// Package server serves a jiggle database over JMAP (RFC 8620), by way of
// Gizmo: the session resource, the API endpoint, the event source, and the
// download of originals and renditions as blobs.  The object types are Photo
// and Album; see doc/types.md.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/rjbs/gizmo/auth"
	"github.com/rjbs/gizmo/httpd"
	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/push"
	"github.com/rjbs/gizmo/sqlitekit"
	"github.com/rjbs/gizmo/std"

	"github.com/rjbs/jiggle/internal/store"
)

// Capability is the capability that Photo and Album are under.
const Capability = "https://semiotic.systems/jmap/jiggle"

// Config configures a Server.
type Config struct {
	Store *store.Store

	// Library is the directory that blob paths are relative to.
	Library string

	// AccountID is the id of the server's one account, "photos" if empty,
	// and Username is its one user, "jiggle" if empty.
	AccountID string
	Username  string

	// BaseURL, if set, is used for the URLs in the session object instead
	// of one inferred from each request.
	BaseURL string

	// PushPollInterval is how often the server looks for changes made by
	// another process, to tell the event source of.  Five seconds if zero.
	PushPollInterval time.Duration

	// MaxSizeUpload is the largest upload taken, in bytes: 16 GiB if
	// zero.
	MaxSizeUpload int64

	// ImageWorkers is how many imports have their images made at once:
	// half the processors if zero.  VideoWorkers is how many videos are
	// encoded at once, one if zero; an encode uses every processor it
	// can.
	ImageWorkers int
	VideoWorkers int

	// Now is the clock, time.Now if nil.
	Now func() time.Time

	Log *slog.Logger
}

// Server is a JMAP server over one jiggle database, with one account.
type Server struct {
	cfg    Config
	store  *store.Store
	dir    *auth.Directory
	broker *push.Broker

	// wake is sent to when there may be imports to work on.
	wake chan struct{}
}

// The most a request may ask for at once.  A library is some tens of
// thousands of photos, and a client may reasonably want all of them.
var limits = std.Limits{MaxObjectsInGet: 5000, MaxObjectsInSet: 1000, MaxQueryLimit: 100000}

// New returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.AccountID == "" {
		cfg.AccountID = "photos"
	}
	if cfg.Username == "" {
		cfg.Username = "jiggle"
	}
	if cfg.PushPollInterval == 0 {
		cfg.PushPollInterval = 5 * time.Second
	}
	if cfg.MaxSizeUpload == 0 {
		cfg.MaxSizeUpload = 16 << 30
	}
	if cfg.ImageWorkers == 0 {
		cfg.ImageWorkers = max(1, runtime.NumCPU()/2)
	}
	if cfg.VideoWorkers == 0 {
		cfg.VideoWorkers = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}

	// One user owns the one account, and is who every request acts as.
	// Who may make requests at all is decided before they get here, by
	// the listener.
	dir, err := auth.NewDirectory(
		map[string]*auth.User{cfg.Username: {Credentials: []auth.Credential{{Type: auth.CredLoopback}}}},
		map[string]*auth.Account{cfg.AccountID: {Owner: cfg.Username, Name: cfg.Username}},
	)
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, store: cfg.Store, dir: dir, broker: push.NewBroker(), wake: make(chan struct{}, 1)}

	// The broker is told the states as they stand, so that the first
	// change published is taken for the one change it is, and not for a
	// change to every type.
	states, err := s.States(context.Background())
	if err != nil {
		return nil, err
	}
	s.broker.Prime(cfg.AccountID, states)
	return s, nil
}

// Capabilities is the account's accountCapabilities object.
func (s *Server) Capabilities() map[string]any {
	return map[string]any{Capability: map[string]any{}}
}

// States gives the state of each type, for the event source.
func (s *Server) States(ctx context.Context) (map[string]string, error) {
	return store.Kit.States(ctx, s.store.DB)
}

// Watch publishes the changes made by writers outside this server, by
// looking for them every PushPollInterval.  It returns when ctx is done.
func (s *Server) Watch(ctx context.Context) {
	push.Watch(ctx, s.broker, s.cfg.AccountID, s.cfg.PushPollInterval, s.States)
}

// Handler returns the server's HTTP handler.  Every request it is given
// acts as the server's one user; whoever lets requests in decides who may
// be that user.
func (s *Server) Handler() http.Handler {
	p := jmap.NewProcessor()
	p.Logger = s.cfg.Log
	find := func(id string) (*Server, bool) { return s, id == s.cfg.AccountID }
	for name, method := range map[string]func(*Server, *jmap.Call, json.RawMessage) (any, error){
		"Photo/get":     (*Server).photoGet,
		"Photo/changes": (*Server).photoChanges,
		"Photo/set":     (*Server).photoSet,
		"Photo/query":   (*Server).photoQuery,
		"Photo/import":  (*Server).photoImport,
		"Album/get":     (*Server).albumGet,
		"Album/changes": (*Server).albumChanges,
		"Album/set":     (*Server).albumSet,
		"Album/query":   (*Server).albumQuery,
	} {
		p.Register(name, Capability, auth.ForAccount(find, method))
	}

	return sameOriginOnly(auth.Loopback(s.dir, httpd.New(httpd.Config{
		BaseURL:   s.cfg.BaseURL,
		Processor: p,
		Directory: s.dir,
		Account: func(id string) httpd.Account {
			if id == s.cfg.AccountID {
				return s
			}
			return nil
		},
		Broker: s.broker,
		Limits: jmap.Limits{
			MaxObjectsInGet: limits.MaxObjectsInGet,
			MaxObjectsInSet: limits.MaxObjectsInSet,
			MaxSizeUpload:   s.cfg.MaxSizeUpload,
		},
		Capabilities: map[string]any{Capability: map[string]any{}},
		Blobs:        s,
		Logger:       s.cfg.Log,
	})))
}

// txn is a transaction on the database, of the kind Gizmo's state kit
// makes.
type txn = sqlitekit.Tx

// begin begins a transaction.  One that writes tells the event source,
// when it commits, of the states it changed.
func (s *Server) begin(ctx context.Context, write bool) (*txn, error) {
	if !write {
		return store.Kit.Read(ctx, s.store.DB)
	}
	return store.Kit.Begin(ctx, s.store.DB, s.broker.Publisher(s.cfg.AccountID))
}

// changes answers a /changes for the type.
func (s *Server) changes(c *jmap.Call, typ string, raw json.RawMessage) (any, error) {
	req, err := std.ParseChanges(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Changes(c, s.begin, req, func(ctx context.Context, tx *txn, since string, max int) (*std.Changed, error) {
		return tx.Changes(ctx, typ, since, max)
	})
}

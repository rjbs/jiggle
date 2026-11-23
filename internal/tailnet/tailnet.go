// Package tailnet joins the server to a Tailscale network as a node of its
// own, using tsnet's userspace networking, so the host needs no tailscaled.
// It provides a listener on the node's MagicDNS name, HTTPS by default with
// a Let's Encrypt certificate Tailscale obtains, and middleware that admits
// only requests from the tailnet users it is told to.
//
// This follows Juke's internal/tailnet, which follows TaskMaster's, and
// ought to give way to one that Gizmo provides.  One lesson from those: a
// node's state belongs to the machine it was made on.  State copied to
// another machine leaves the node stuck in Starting, with the health
// message "node OS changed since last connection".  So each machine is its
// own node, under its own name (jiggle on the server, jiggle-dev on a laptop),
// and state is never moved.  -- claude, 2026-11-23
package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// Node is this server's node on the tailnet.
type Node struct {
	srv *tsnet.Server
	lc  *local.Client

	// DNSName is the node's MagicDNS name, like "jiggle.example.ts.net".
	DNSName string
}

// Start brings the node up and waits until it is connected.  The first time
// a node runs it must be authorized: tsnet uses an auth key from the
// TS_AUTHKEY environment variable if there is one, and otherwise logs a
// login URL to visit.  State is kept in dir, so later starts need no
// authorization.  With verbose, tsnet's own chatty log goes to logger too.
func Start(ctx context.Context, hostname, dir string, logger *slog.Logger, verbose bool) (*Node, error) {
	srv := &tsnet.Server{
		Hostname: hostname,
		Dir:      dir,
		UserLogf: func(format string, args ...any) {
			logf(logger, "tailscale: "+format, args...)
		},
		Logf: func(format string, args ...any) {
			if verbose {
				logf(logger, "tailscale: "+format, args...)
			}
		},
	}
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, err
	}
	stopReporting := reportWhileStarting(ctx, lc, logger)
	status, err := srv.Up(ctx)
	stopReporting()
	if err != nil {
		srv.Close()
		return nil, err
	}
	n := &Node{srv: srv, lc: lc}
	if status.Self != nil {
		n.DNSName = trimDot(status.Self.DNSName)
	}
	return n, nil
}

// Listen listens on the node: with https, on port 443 with a certificate
// for the node's MagicDNS name, which needs both MagicDNS and HTTPS
// certificates enabled in the tailnet's admin console; otherwise on port
// 80 in plain HTTP.  The first HTTPS connection waits, perhaps half a
// minute, while the certificate is obtained.
func (n *Node) Listen(https bool) (net.Listener, error) {
	if https {
		return n.srv.ListenTLS("tcp", ":443")
	}
	return n.srv.Listen("tcp", ":80")
}

// URL is the base URL clients reach the node at.
func (n *Node) URL(https bool) string {
	if https {
		return "https://" + n.DNSName
	}
	return "http://" + n.DNSName
}

// Identity is who a request comes from: the login of the user whose node
// sent it, like "rjbs@github", and whether that node is tagged, which
// makes it belong to no user.
type Identity struct {
	Login  string
	Tagged bool
}

// WhoIs resolves a connection's remote address to the identity behind it.
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	who, err := n.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{Tagged: who.Node != nil && who.Node.IsTagged()}
	if who.UserProfile != nil {
		id.Login = who.UserProfile.LoginName
	}
	return id, nil
}

// Close takes the node off the tailnet.
func (n *Node) Close() error {
	return n.srv.Close()
}

// RequireLogin admits a request only if whois says it comes from an
// untagged node of one of the allowed logins, which are whole login names
// like "rjbs@github".  Anything else gets 401 if it can't be identified,
// or 403, and is logged with its login, so a login that should have been
// allowed can be added.
func RequireLogin(whois func(context.Context, string) (Identity, error), allowed []string, logger *slog.Logger, next http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, login := range allowed {
		allow[login] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := whois(r.Context(), r.RemoteAddr)
		switch {
		case err != nil:
			logf(logger, "refused %s %s from %s: can't identify it: %v", r.Method, r.URL.Path, r.RemoteAddr, err)
			http.Error(w, "unidentified", http.StatusUnauthorized)
		case id.Tagged:
			logf(logger, "refused %s %s from %s: a tagged node", r.Method, r.URL.Path, r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
		case !allow[id.Login]:
			logf(logger, "refused %s %s from %s: login %q is not allowed", r.Method, r.URL.Path, r.RemoteAddr, id.Login)
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// reportWhileStarting logs the node's backend state and health every ten
// seconds until the returned function is called.  tsnet's Up blocks
// silently while the node can't reach the control server, and its own log
// is too chatty to show by default, so without this a node stuck in
// Starting looks exactly like one that's fine.  (From TaskMaster.)
func reportWhileStarting(ctx context.Context, lc *local.Client, logger *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			st, err := lc.StatusWithoutPeers(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logf(logger, "tailscale node not yet running: %v", err)
				}
				continue
			}
			if st.BackendState == "Running" {
				continue
			}
			logf(logger, "tailscale node not yet running: state %s, health %v, login URL %q",
				st.BackendState, st.Health, st.AuthURL)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

func logf(logger *slog.Logger, format string, args ...any) {
	logger.Info(fmt.Sprintf(format, args...))
}

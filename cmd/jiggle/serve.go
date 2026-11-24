package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/rjbs/gizmo/tailnet"

	"github.com/rjbs/jiggle/internal/server"
	"github.com/rjbs/jiggle/internal/store"
)

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "", "the database")
	library := fs.String("library", "", "the library, where the originals and renditions are")
	listen := fs.String("listen", "localhost:8739", "the address to listen on in plain HTTP, with no authentication; empty for none")
	baseURL := fs.String("base-url", "", "the URL clients reach the server at, if not the request's own")
	tailnetName := fs.String("tailnet", "", "join the tailnet as a node of this name, such as jiggle")
	tailnetState := fs.String("tailnet-state", "", "the directory the tailnet node keeps its state in; never copy it to another machine")
	tailnetHTTP := fs.Bool("tailnet-http", false, "serve the tailnet in plain HTTP on port 80, not HTTPS on 443")
	tailnetVerbose := fs.Bool("tailnet-verbose", false, "log everything tsnet says")
	editorDir := fs.String("editor-dir", "", "serve the editor from this copy of the repository's share directory, not the one built in")
	var allow listFlag
	fs.Var(&allow, "allow", "a tailnet login allowed in, like someone@github; repeatable, and needed with --tailnet")
	parse(fs, args, "db", "library")

	if *tailnetName != "" && (*tailnetState == "" || len(allow) == 0) {
		return errors.New("--tailnet needs --tailnet-state and at least one --allow")
	}
	if *tailnetName == "" && *listen == "" {
		return errors.New("with neither --listen nor --tailnet there is nothing to serve on")
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv, err := server.New(server.Config{Store: s, Library: *library, BaseURL: *baseURL, EditorDir: *editorDir, Log: logger})
	if err != nil {
		return err
	}
	go srv.Watch(ctx)
	go srv.Work(ctx)
	handler := srv.Handler()

	// Each listener runs until it fails, and the first failure ends the
	// server.
	failed := make(chan error, 2)

	if *listen != "" {
		logger.Info("serving", "db", *dbPath, "url", "http://"+*listen+"/")
		named, _, _ := net.SplitHostPort(*listen)
		srv.Guard().AllowHost(named)
		go func() { failed <- http.ListenAndServe(*listen, handler) }()
	}

	if *tailnetName != "" {
		node, err := tailnet.Start(ctx, tailnet.Config{Hostname: *tailnetName, Dir: *tailnetState, Logger: logger, Verbose: *tailnetVerbose})
		if err != nil {
			return fmt.Errorf("joining the tailnet: %w", err)
		}
		defer node.Close()
		ln, err := node.Listen(!*tailnetHTTP)
		if err != nil {
			return fmt.Errorf("listening on the tailnet: %w", err)
		}
		logger.Info("serving", "db", *dbPath, "url", node.URL(!*tailnetHTTP)+"/", "to", allow.String())
		srv.Guard().AllowHost(node.Names()...)
		guarded := node.RequireLogin(allow, handler)
		go func() { failed <- http.Serve(ln, guarded) }()
	}

	return <-failed
}

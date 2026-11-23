package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"

	"github.com/rjbs/jiggle/internal/server"
	"github.com/rjbs/jiggle/internal/store"
)

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "", "the database")
	library := fs.String("library", "", "the library, where the originals and renditions are")
	listen := fs.String("listen", "localhost:8739", "the address to listen on in plain HTTP, with no authentication")
	baseURL := fs.String("base-url", "", "the URL clients reach the server at, if not the request's own")
	parse(fs, args, "db", "library", "listen")

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv, err := server.New(server.Config{Store: s, Library: *library, BaseURL: *baseURL, Log: logger})
	if err != nil {
		return err
	}
	go srv.Watch(ctx)

	logger.Info("serving", "db", *dbPath, "url", "http://"+*listen+"/")
	return http.ListenAndServe(*listen, srv.Handler())
}

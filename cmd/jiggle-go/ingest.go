package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/rjbs/jiggle/internal/ingest"
)

// runIngest brings files into the library of a running server, as a
// client of it: see the ingest package.  Its arguments are files, and
// directories whose files are meant.
//
// The server is found at --url, or at JIGGLE_URL in the environment, or
// where jiggle-go serve listens if it's told nothing.
func runIngest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: jiggle-go ingest [options] FILE-OR-DIR...")
		fs.PrintDefaults()
	}
	at := os.Getenv("JIGGLE_URL")
	if at == "" {
		at = "http://localhost:8739"
	}
	base := fs.String("url", at, "the server (default: JIGGLE_URL in the environment, if it's set)")
	noWait := fs.Bool("no-wait", false, "don't wait for the server to finish importing what was sent")
	discard := fs.Bool("discard-failed", false, "destroy the new photos whose imports fail, instead of leaving them to be dealt with")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	if *noWait && *discard {
		return errors.New("--discard-failed has to wait to see what fails, so it can't go with --no-wait")
	}

	paths, err := ingest.Paths(fs.Args())
	if err != nil {
		return err
	}
	client, err := ingest.Connect(ctx, *base)
	if err != nil {
		return err
	}
	in := &ingest.Ingest{Client: client, Log: func(message string) { fmt.Println(message) }}

	// An interrupt stops sending files, or stops waiting.  What was sent
	// is imported regardless.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	files, err := in.Files(ctx, paths)
	if err == nil && !*noWait {
		err = in.Wait(ctx, files)
	}
	if err == nil && *discard {
		err = in.Discard(ctx, files)
	}

	count := map[string]int{}
	for _, f := range files {
		count[f.Status]++
	}
	made := count[ingest.Ingested] + count[ingest.Failed] + count[ingest.Discarded]
	fmt.Printf("ingested %d of %d file(s)", made, len(paths))
	for _, part := range []struct{ status, what string }{
		{ingest.Exists, "already in the library"},
		{ingest.Skipped, "skipped"},
		{ingest.Failed, "failed to import"},
		{ingest.Discarded, "failed to import and discarded"},
		{ingest.Unsent, "not sent"},
	} {
		if count[part.status] > 0 {
			fmt.Printf("; %d %s", count[part.status], part.what)
		}
	}
	fmt.Println()
	if made > count[ingest.Discarded] {
		fmt.Println("to review them:", client.BaseURL+"/?q=pending")
	}

	switch {
	case errors.Is(err, context.Canceled) && len(files) < len(paths):
		return fmt.Errorf("interrupted with %d file(s) not yet looked at", len(paths)-len(files))
	case errors.Is(err, context.Canceled):
		return errors.New("interrupted; the server carries on importing")
	case err != nil:
		return err
	case count[ingest.Unsent] > 0:
		return fmt.Errorf("%d file(s) could not be sent", count[ingest.Unsent])
	}
	return nil
}

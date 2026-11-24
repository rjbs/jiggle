package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/rjbs/jiggle/internal/site"
)

func runBuild(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	var opts site.Options
	fs.StringVar(&opts.DB, "db", "", "the database")
	fs.StringVar(&opts.Library, "library", "", "the library, where the renditions are")
	fs.StringVar(&opts.Out, "out", "", "the directory to build the site in")
	fs.StringVar(&opts.Manifest, "manifest", "", "the record of what the last build made (default OUT.manifest.json)")
	fs.BoolVar(&opts.Verify, "verify", false, "check files on disk instead of trusting the manifest")
	noSearch := fs.Bool("no-search", false, "don't build the search index (it needs Pagefind, via npx)")
	parse(fs, args, "db", "library", "out")

	if !*noSearch {
		opts.Search = &site.Search{}
	}

	// Every message gets the time of day, so a long run's log shows where
	// the time went.
	opts.Log = func(message string) {
		fmt.Println(time.Now().Format("[15:04:05] ") + message)
	}
	return site.Build(ctx, opts)
}

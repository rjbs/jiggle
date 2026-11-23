package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/rjbs/jiggle/internal/migrate"
)

func runMigrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	var opts migrate.Options
	fs.StringVar(&opts.Library, "library", "", "the TOML library, as Perl jiggle keeps it")
	fs.StringVar(&opts.Out, "out", "", "the new database; must not exist")
	parse(fs, args, "library", "out")

	r, err := migrate.Run(ctx, opts)
	if err != nil {
		return err
	}
	for _, warning := range r.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", warning)
	}
	fmt.Printf("photos:     %d\n", r.Photos)
	fmt.Printf("videos:     %d\n", r.Videos)
	fmt.Printf("albums:     %d\n", r.Albums)
	fmt.Printf("renditions: %d\n", r.Renditions)
	return nil
}

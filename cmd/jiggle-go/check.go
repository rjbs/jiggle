package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/rjbs/jiggle/internal/migrate"
)

func runCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dbPath := fs.String("db", "", "the database migrate made")
	dumpPath := fs.String("dump", "", "what \"jiggle dump\" printed, for the same library (default standard input)")
	parse(fs, args, "db")

	var dump io.Reader = os.Stdin
	if *dumpPath != "" {
		file, err := os.Open(*dumpPath)
		if err != nil {
			return err
		}
		defer file.Close()
		dump = file
	}

	r, err := migrate.Check(ctx, *dbPath, dump)
	if err != nil {
		return err
	}
	for _, difference := range r.Differences {
		fmt.Println(difference)
	}
	fmt.Printf("photos: %d\n", r.Photos)
	fmt.Printf("albums: %d\n", r.Albums)
	if n := len(r.Differences); n > 0 {
		return fmt.Errorf("the database and Perl jiggle differ in %d place(s)", n)
	}
	fmt.Println("the database and Perl jiggle agree")
	return nil
}

package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/rjbs/jiggle/internal/sitediff"
)

func runSiteDiff(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sitediff", flag.ExitOnError)
	opts := sitediff.Options{NameA: "Perl", NameB: "Go"}
	fs.StringVar(&opts.A, "perl", "", "the site Perl jiggle built")
	fs.StringVar(&opts.B, "go", "", "the site jiggle-go built, from a migration of the same library")
	show := fs.Int("show", 20, "how many differences to print")
	equivalent := fs.Bool("equivalent", false, "list the files that differ in their bytes but not their meaning")
	search := fs.Bool("search", false, "compare the search indexes, too")
	parse(fs, args, "perl", "go")

	// A search index is made from the pages' bytes, so two builders whose
	// pages differ at all make different indexes.
	if !*search {
		opts.Skip = []string{"pagefind/"}
	}
	opts.Sizes = []string{"img/"}

	r, err := sitediff.Compare(opts)
	if err != nil {
		return err
	}
	for i, difference := range r.Differences {
		if i == *show {
			fmt.Printf("... and %d more\n", len(r.Differences)-i)
			break
		}
		fmt.Println(difference)
	}
	if *equivalent {
		for _, rel := range r.Equivalent {
			fmt.Printf("%s: equivalent\n", rel)
		}
	}
	fmt.Printf("files:      %d\n", r.Files)
	fmt.Printf("identical:  %d\n", r.Identical)
	fmt.Printf("equivalent: %d\n", len(r.Equivalent))
	fmt.Printf("different:  %d\n", len(r.Differences))
	if n := len(r.Differences); n > 0 {
		return fmt.Errorf("the sites differ in %d file(s)", n)
	}
	return nil
}

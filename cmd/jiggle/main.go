// Command jiggle is jiggle: its server and its tools.
//
//	jiggle build --db FILE --library DIR --out DIR [--no-search] [--verify]
//	jiggle serve --db FILE --library DIR [--listen ADDR] [--tailnet NAME ...]
//	jiggle backup --db FILE --to DIR [--keep N]
//	jiggle sync --db FILE --site DIR [--to rsync|bunny] [--dry-run] [--purge-all]
//	jiggle ingest [--url URL] [--no-wait] [--discard-failed] FILE-OR-DIR...
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "build":
		err = runBuild(ctx, os.Args[2:])
	case "sync":
		err = runSync(ctx, os.Args[2:])
	case "backup":
		err = runBackup(ctx, os.Args[2:])
	case "ingest":
		err = runIngest(ctx, os.Args[2:])
	case "serve":
		err = runServe(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jiggle:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: jiggle build|serve|backup|sync|ingest [options]; jiggle COMMAND -h for options")
	os.Exit(2)
}

func parse(fs *flag.FlagSet, args []string, required ...string) {
	fs.Parse(args)
	for _, name := range required {
		if fs.Lookup(name).Value.String() == "" {
			fmt.Fprintf(os.Stderr, "jiggle %s: --%s is required\n", fs.Name(), name)
			fs.Usage()
			os.Exit(2)
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "jiggle %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		fs.Usage()
		os.Exit(2)
	}
}

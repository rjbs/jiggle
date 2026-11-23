// Command jiggle-go is jiggle's server and its tools, written in Go.  It
// has this name until it takes over from Perl jiggle, and then it is jiggle.
//
//	jiggle-go migrate --library DIR --out FILE
//	jiggle-go check --db FILE [--dump FILE]
//	jiggle-go build --db FILE --library DIR --out DIR [--no-search] [--verify]
//	jiggle-go sitediff --perl DIR --go DIR
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
	case "migrate":
		err = runMigrate(ctx, os.Args[2:])
	case "check":
		err = runCheck(ctx, os.Args[2:])
	case "build":
		err = runBuild(ctx, os.Args[2:])
	case "sitediff":
		err = runSiteDiff(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jiggle-go:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: jiggle-go migrate|check|build|sitediff [options]; jiggle-go COMMAND -h for options")
	os.Exit(2)
}

func parse(fs *flag.FlagSet, args []string, required ...string) {
	fs.Parse(args)
	for _, name := range required {
		if fs.Lookup(name).Value.String() == "" {
			fmt.Fprintf(os.Stderr, "jiggle-go %s: --%s is required\n", fs.Name(), name)
			fs.Usage()
			os.Exit(2)
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "jiggle-go %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		fs.Usage()
		os.Exit(2)
	}
}

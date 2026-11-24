package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/rjbs/jiggle/internal/store"
)

func runBackup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "", "the database, which a server may be running on")
	dir := fs.String("to", "", "the directory to put the backup in, named for the time")
	keep := fs.Int("keep", 0, "afterward, remove all but this many of the newest backups there (default: remove none)")
	parse(fs, args, "db", "to")
	if *keep < 0 {
		return errors.New("--keep can't be negative")
	}

	path, err := store.Backup(ctx, *dbPath, *dir, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(path)

	if *keep > 0 {
		removed, err := store.PruneBackups(*dir, *keep)
		for _, path := range removed {
			fmt.Println("removed", path)
		}
		return err
	}
	return nil
}

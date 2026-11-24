package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/rjbs/jiggle/internal/publish"
	"github.com/rjbs/jiggle/internal/store"
)

// runSync copies a built site to where the database says it's published:
// a web server reached by rsync, or a Bunny storage zone.  See the publish
// package.
//
// Bunny needs two secrets, the storage zone's password and the account's
// API key (for purging the CDN's cache).  They're read from the
// environment, as BUNNY_STORAGE_PASSWORD and BUNNY_API_KEY.
//
// It refuses to run after an interrupted build, whose output may be half
// updated.
func runSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	dbPath := fs.String("db", "", "the database, which says where the site is published")
	site := fs.String("site", "", "the built site")
	manifest := fs.String("manifest", "", "the record of what the last build made (default SITE.manifest.json)")
	record := fs.String("record", "", "with bunny, the record of what has been uploaded (default bunny-uploaded.json, beside the manifest)")
	to := fs.String("to", "", "which target to sync to, if more than one: rsync or bunny")
	dryRun := fs.Bool("dry-run", false, "show what would be copied or removed, but change nothing")
	fs.BoolVar(dryRun, "n", false, "the same as --dry-run")
	purgeAll := fs.Bool("purge-all", false, "with bunny, purge the whole pull zone from the CDN, not just what changed")
	parse(fs, args, "db", "site")

	if *manifest == "" {
		*manifest = strings.TrimRight(*site, "/") + ".manifest.json"
	}
	if *record == "" {
		*record = filepath.Join(filepath.Dir(*manifest), "bunny-uploaded.json")
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	config, err := publish.LoadConfig(ctx, s.DB)
	s.Close()
	if err != nil {
		return err
	}

	targets := config.Targets()
	switch {
	case len(targets) == 0:
		return errors.New("nothing to sync to: the database has no publish.rsync.target and no publish.bunny.zone setting")
	case *to == "" && len(targets) > 1:
		return errors.New("both rsync and bunny targets are configured; choose one with --to")
	case *to == "":
		*to = targets[0]
	case *to != "rsync" && *to != "bunny":
		return errors.New("--to must be rsync or bunny")
	case !strings.Contains(strings.Join(targets, " "), *to):
		return fmt.Errorf("the database has no %s target", *to)
	}

	if info, err := os.Stat(*site); err != nil || !info.IsDir() {
		return fmt.Errorf("no site at %s; run jiggle build first", *site)
	}
	if _, err := os.Stat(*manifest + ".building"); err == nil {
		return errors.New("the last build didn't finish, so the site may be half updated; build again first")
	}

	// An interrupt stops a sync where it is, with what it did recorded
	// and purged.  A second one stops it dead.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	log := func(message string) {
		fmt.Println(time.Now().Format("[15:04:05] ") + message)
	}

	if *to == "rsync" {
		rsync := &publish.Rsync{
			SiteDir: *site, Target: config.RsyncTarget, Command: config.RsyncCommand, DryRun: *dryRun,
			Log: log, Stdout: os.Stdout, Stderr: os.Stderr,
		}
		return rsync.Sync(ctx)
	}

	bunny := &publish.Bunny{
		SiteDir: *site, Manifest: *manifest, Record: *record,
		Zone: config.BunnyZone, StorageURL: config.BunnyStorageURL,
		PullZoneID: config.BunnyPullZoneID, PurgeHosts: config.BunnyPurgeHosts,
		Password: os.Getenv("BUNNY_STORAGE_PASSWORD"), APIKey: os.Getenv("BUNNY_API_KEY"),
		Log: log,
	}
	// A dry run makes no requests, so it needs no secrets.
	if !*dryRun {
		switch {
		case bunny.Password == "":
			return errors.New("no Bunny storage password: set BUNNY_STORAGE_PASSWORD")
		case bunny.APIKey == "":
			return errors.New("no Bunny API key: set BUNNY_API_KEY")
		case bunny.PullZoneID == "":
			return errors.New("the database has no publish.bunny.pull_zone_id setting")
		}
	}

	result, err := bunny.Sync(ctx, publish.SyncOptions{DryRun: *dryRun, PurgeAll: *purgeAll})
	if result == nil {
		return err
	}
	if *dryRun {
		for _, rel := range result.Uploaded {
			fmt.Println("upload", rel)
		}
		for _, rel := range result.Deleted {
			fmt.Println("delete", rel)
		}
		log(fmt.Sprintf("dry run done; would upload %d and delete %d, then purge them", len(result.Uploaded), len(result.Deleted)))
		return err
	}

	purged := fmt.Sprintf("%d purge request(s)", result.Purged)
	if result.PurgedAll {
		purged = "purged the whole pull zone"
	}
	log(fmt.Sprintf("synced: %d uploaded, %d deleted; %s", len(result.Uploaded), len(result.Deleted), purged))
	return err
}

package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rsyncRuns checks the command that a sync runs, given an rsync that says
// it's the given version: a script that answers --version and otherwise
// writes down its arguments.
func rsyncRuns(t *testing.T, version string, dryRun bool, want string) {
	t.Helper()
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "rsync")
	body := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\necho \"$@\" > " + ran + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Rsync{SiteDir: "/library/site/", Target: "host:/var/www/", Command: script, DryRun: dryRun}
	if err := r.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(ran)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("with %q, dry run %v, it ran\n got: %s\nwant: %s", version, dryRun, got, want)
	}
}

func TestRsync(t *testing.T) {
	const (
		modern = "rsync  version 3.2.7  protocol version 31"
		old    = "openrsync: protocol version 29\nrsync version 2.6.9 compatible"
		common = "--recursive --links --times --compress "
		ends   = "/library/site/ host:/var/www/"
	)
	rsyncRuns(t, modern, false, common+"--delete-delay --info=progress2,stats1 --human-readable "+ends)
	rsyncRuns(t, modern, true, common+"--delete-delay --info=progress2,stats1 --human-readable --dry-run --itemize-changes "+ends)
	rsyncRuns(t, old, false, common+"--delete --verbose "+ends)
	rsyncRuns(t, old, true, common+"--delete --verbose --dry-run "+ends)
	rsyncRuns(t, "rsync  version 4.0.0", false, common+"--delete-delay --info=progress2,stats1 --human-readable "+ends)
}

func TestRsyncThatFails(t *testing.T) {
	r := &Rsync{SiteDir: "/library/site", Target: "host:/var/www/", Command: "false"}
	if err := r.Sync(context.Background()); err == nil {
		t.Error("an rsync that failed was taken for one that worked")
	}
}

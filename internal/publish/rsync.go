package publish

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Rsync publishes a site to a web server, by running rsync.
//
// Files removed from the site are removed from the target too, which is
// how a photo made private leaves the published site.  Unchanged files are
// skipped by size and modification time; builds leave unchanged files
// alone, so that's accurate, and much faster than comparing contents.
type Rsync struct {
	// SiteDir is the built site, and Target where to copy it, like
	// "user@example.com:/var/www/photos/".
	SiteDir string
	Target  string

	// Command is the rsync to run, "rsync" if empty.
	Command string

	// DryRun shows what would be copied or removed, and changes
	// nothing.
	DryRun bool

	Log            func(string)
	Stdout, Stderr io.Writer
}

var rsyncVersionRE = regexp.MustCompile(`\Arsync\s+version\s+([0-9]+)\.`)

// Sync runs rsync.
func (r *Rsync) Sync(ctx context.Context) error {
	command := r.Command
	if command == "" {
		command = "rsync"
	}
	log := r.Log
	if log == nil {
		log = func(string) {}
	}

	// macOS's own rsync is openrsync, which has only the basic options.
	// With rsync 3, deletions wait until the new files are in place, so
	// the live site never links to pages already gone, and progress is
	// one line and a summary rather than a list of every file.
	modern := false
	if out, err := exec.CommandContext(ctx, command, "--version").Output(); err == nil {
		if m := rsyncVersionRE.FindSubmatch(out); m != nil {
			major, _ := strconv.Atoi(string(m[1]))
			modern = major >= 3
		}
	}

	args := []string{"--recursive", "--links", "--times", "--compress"}
	if modern {
		args = append(args, "--delete-delay", "--info=progress2,stats1", "--human-readable")
	} else {
		args = append(args, "--delete", "--verbose")
	}
	if r.DryRun {
		args = append(args, "--dry-run")
		if modern {
			args = append(args, "--itemize-changes")
		}
	}
	args = append(args, strings.TrimRight(r.SiteDir, "/")+"/", r.Target)

	log(command + " " + strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout, cmd.Stderr = r.Stdout, r.Stderr
	if err := cmd.Run(); err != nil {
		return errors.New("rsync failed: " + err.Error())
	}
	if r.DryRun {
		log("dry run done; nothing changed")
	} else {
		log("synced")
	}
	return nil
}

package site

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PagefindVersion is the Pagefind that builds the index.  It's pinned, so
// that a new release can't change the index format (and so every index
// file) without our noticing.
const PagefindVersion = "1.5.2"

// Search builds the site's search index with Pagefind, which reads the
// built HTML and produces a static index, split into chunks so that a
// search downloads only the parts it needs.  Its output is deterministic,
// and its chunk files are named by their content, so an unchanged site
// gives an unchanged index.
//
// Only what's in the site can be indexed, so private photos are never in
// the index, provided stale pages were pruned before Pagefind ran.
type Search struct {
	// Command is how Pagefind is run.  If empty, it is the pagefind on
	// PATH, whatever its version, or, if there is none, npx and the
	// pinned version, which needs Node.
	Command []string
}

func (s *Search) command() []string {
	if len(s.Command) > 0 {
		return s.Command
	}
	if _, err := exec.LookPath("pagefind"); err == nil {
		return []string{"pagefind"}
	}
	return []string{"npx", "--yes", "pagefind@" + PagefindVersion}
}

// IndexSite runs Pagefind over the HTML in the site directory, then writes
// its output under pagefind/ through the writer, so that a file that hasn't
// changed is left alone, like everything else.  It returns how many files
// the index is.
func (s *Search) IndexSite(siteDir string, w *Writer) (int, error) {
	scratch, err := os.MkdirTemp("", "jiggle-pagefind-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(scratch)

	command := append(s.command(), "--site", siteDir, "--output-path", scratch, "--silent")
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("pagefind failed: %s: %w", strings.Join(command, " "), err)
	}

	n := 0
	err = filepath.WalkDir(scratch, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(scratch, path)
		if err != nil {
			return err
		}
		n++
		return w.WriteFile("pagefind/"+filepath.ToSlash(rel), content)
	})
	return n, err
}

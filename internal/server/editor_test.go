package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serves checks that the server answers a GET of path with the given
// content type and a body that has the given text in it.
func serves(t *testing.T, f *fixture, path, contentType, text string) {
	t.Helper()
	res, err := http.Get(f.BaseURL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Errorf("GET %s: status %d", path, res.StatusCode)
		return
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, contentType) {
		t.Errorf("GET %s: content type %q, want %s", path, got, contentType)
	}
	if !strings.Contains(string(body), text) {
		t.Errorf("GET %s: no %q in it", path, text)
	}
}

func TestEditorFiles(t *testing.T) {
	f := newFixture(t)
	serves(t, f, "/", "text/html", `src="/editor/editor.mjs"`)
	serves(t, f, "/?q=pending", "text/html", `src="/editor/editor.mjs"`)
	serves(t, f, "/editor/editor.mjs", "text/javascript", `from "./jmap.mjs"`)
	serves(t, f, "/editor/jmap.mjs", "text/javascript", "export class Client")
	serves(t, f, "/editor/model.mjs", "text/javascript", "export function parseQuery")
	serves(t, f, "/editor/editor.css", "text/css", "#sheet")

	// The API is still where it was.
	serves(t, f, "/.well-known/jmap", "application/json", Capability)
}

// Every file the page asks for by name is one the server serves.
func TestEditorFilesAreAllServed(t *testing.T) {
	for _, name := range editorFiles {
		if _, err := os.Stat(filepath.Join("..", "..", "share", name)); err != nil {
			t.Errorf("the editor serves %s, and there is no such file", name)
		}
	}
	entries, err := os.ReadDir(filepath.Join("..", "..", "share", "editor-jmap"))
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, name := range editorFiles {
		served[name] = true
	}
	for _, entry := range entries {
		if name := "editor-jmap/" + entry.Name(); !served[name] {
			t.Errorf("share/%s is not served", name)
		}
	}
}

func TestEditorDir(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"editor-jmap/index.html": "<p>a page being worked on"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixtureWith(t, func(cfg *Config) { cfg.EditorDir = dir })
	serves(t, f, "/", "text/html", "a page being worked on")
}

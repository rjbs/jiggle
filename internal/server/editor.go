package server

import (
	"io/fs"
	"net/http"
	"os"

	"github.com/rjbs/jiggle/share"
)

// editorFiles are the editor's files, by the path each is served at, as
// paths in share.Editor.  The page itself is served at /.
var editorFiles = map[string]string{
	"/{$}":               "editor-jmap/index.html",
	"/editor/editor.css": "editor/static/editor.css",
	"/editor/editor.mjs": "editor-jmap/editor.mjs",
	"/editor/jmap.mjs":   "editor-jmap/jmap.mjs",
	"/editor/model.mjs":  "editor-jmap/model.mjs",
}

// withEditor serves the editor's page and its files, and gives every other
// request to next.  The editor is a JMAP client like any other: the page
// has nothing in it but what it gets from the API.
//
// The files are the ones built into the program, or, with an EditorDir,
// the ones in that copy of the share directory, read on every request, so
// that the editor can be worked on without building the server again.
func (s *Server) withEditor(next http.Handler) http.Handler {
	var files fs.FS = share.Editor
	if s.cfg.EditorDir != "" {
		files = os.DirFS(s.cfg.EditorDir)
	}
	mux := http.NewServeMux()
	for pattern, name := range editorFiles {
		mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, files, name)
		})
	}
	mux.Handle("/", next)
	return mux
}

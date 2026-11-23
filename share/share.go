// Package share holds the files the site is published with: its
// stylesheet and scripts.  They are the ones Perl jiggle publishes.  It
// holds the editor's files, too.
package share

import "embed"

// Static is the directory static, whose files are published under /static/.
// Files whose names begin with a dot or an underscore are left out.
//
//go:embed static
var Static embed.FS

// Editor holds the editor that the server serves: the page and its scripts
// in editor-jmap, and the stylesheet it shares with Perl jiggle's editor,
// editor/static/editor.css.
//
//go:embed editor-jmap editor/static/editor.css
var Editor embed.FS

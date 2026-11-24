// Package share holds the files the site is published with, its
// stylesheet and scripts, and the editor's files.
package share

import "embed"

// Static is the directory static, whose files are published under /static/.
// Files whose names begin with a dot or an underscore are left out.
//
//go:embed static
var Static embed.FS

// Editor holds the editor that the server serves, in the directory editor:
// the page, its scripts and its stylesheet.
//
//go:embed editor
var Editor embed.FS

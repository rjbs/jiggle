// Package share holds the files the site is published with: its
// stylesheet and scripts.  They are the ones Perl jiggle publishes.
package share

import "embed"

// Static is the directory static, whose files are published under /static/.
// Files whose names begin with a dot or an underscore are left out.
//
//go:embed static
var Static embed.FS

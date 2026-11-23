// Package sitediff compares two built sites, to check the site builder in
// Go against the one in Perl: each builds the site from the same library,
// and what differs is either a bug or a change made on purpose.
//
// Two files are the same if their bytes are.  Failing that, two pages are
// the same if they are the same HTML: the two builders' templates escape
// text differently (a quotation mark is &quot; from one and &#34; from the
// other), which changes the bytes of most pages and the meaning of none.
// Two JSON files are the same if they hold the same data.  Renditions are
// compared only by size, since each is a link to the same file.
package sitediff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Report says how two sites compare.
type Report struct {
	// Files is how many files the two sites have between them.
	Files int

	// Identical is how many are the same bytes in both.
	Identical int

	// Equivalent is the files that differ in their bytes but not in what
	// they mean.
	Equivalent []string

	// Differences are the files that are in only one site, or that mean
	// different things, each with where and how.
	Differences []string
}

// Options says what to compare.
type Options struct {
	// A and B are the two sites' directories, and NameA and NameB what to
	// call them.
	A, B         string
	NameA, NameB string

	// Skip is path prefixes, with slashes, to leave out of both sites.
	Skip []string

	// Sizes is path prefixes of files to compare only by size.
	Sizes []string
}

// Compare compares two sites.
func Compare(opts Options) (*Report, error) {
	a, err := list(opts.A, opts.Skip)
	if err != nil {
		return nil, err
	}
	b, err := list(opts.B, opts.Skip)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(a))
	for rel := range a {
		paths = append(paths, rel)
	}
	for rel := range b {
		if _, ok := a[rel]; !ok {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)

	r := &Report{Files: len(paths)}
	for _, rel := range paths {
		sizeA, inA := a[rel]
		sizeB, inB := b[rel]
		switch {
		case !inB:
			r.differ("%s: only in %s", rel, opts.NameA)
			continue
		case !inA:
			r.differ("%s: only in %s", rel, opts.NameB)
			continue
		}

		if hasPrefix(rel, opts.Sizes) {
			if sizeA != sizeB {
				r.differ("%s: %d bytes in %s, %d in %s", rel, sizeA, opts.NameA, sizeB, opts.NameB)
			} else {
				r.Identical++
			}
			continue
		}

		dataA, err := os.ReadFile(filepath.Join(opts.A, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		dataB, err := os.ReadFile(filepath.Join(opts.B, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if bytes.Equal(dataA, dataB) {
			r.Identical++
			continue
		}

		var how string
		switch filepath.Ext(rel) {
		case ".html":
			how = diffHTML(dataA, dataB, opts.NameA, opts.NameB)
		case ".json", ".geojson":
			how = diffJSON(dataA, dataB)
		default:
			how = "the bytes differ"
		}
		if how == "" {
			r.Equivalent = append(r.Equivalent, rel)
		} else {
			r.differ("%s: %s", rel, how)
		}
	}
	return r, nil
}

func (r *Report) differ(format string, args ...any) {
	r.Differences = append(r.Differences, fmt.Sprintf(format, args...))
}

func hasPrefix(rel string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// list returns the size of every file under root, by its path from there.
func list(root string, skip []string) (map[string]int64, error) {
	files := map[string]int64{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if hasPrefix(rel, skip) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[rel] = info.Size()
		return nil
	})
	return files, err
}

func diffJSON(a, b []byte) string {
	var dataA, dataB any
	if err := json.Unmarshal(a, &dataA); err != nil {
		return "the bytes differ, and one is not JSON"
	}
	if err := json.Unmarshal(b, &dataB); err != nil {
		return "the bytes differ, and one is not JSON"
	}
	if reflect.DeepEqual(dataA, dataB) {
		return ""
	}
	return "the data differs"
}

// A token is one piece of a page: a tag with its attributes, or some text.
type token struct {
	kind  html.TokenType
	data  string
	attrs []html.Attribute
}

func (t token) String() string {
	switch t.kind {
	case html.TextToken:
		return fmt.Sprintf("the text %q", t.data)
	case html.EndTagToken:
		return "</" + t.data + ">"
	case html.StartTagToken, html.SelfClosingTagToken:
		var out strings.Builder
		out.WriteString("<" + t.data)
		for _, a := range t.attrs {
			fmt.Fprintf(&out, " %s=%q", a.Key, a.Val)
		}
		return out.String() + ">"
	}
	return fmt.Sprintf("%s %q", t.kind, t.data)
}

// urlAttrs are the attributes whose values are URLs, in which a character
// may be written as itself or as its percent-encoding.
var urlAttrs = map[string]bool{"href": true, "src": true, "poster": true, "srcset": true}

// tokens takes a page apart.  Entities are decoded, so that it doesn't
// matter which of the ways to write a character was used, and so is the
// percent-encoding of URLs.
func tokens(page []byte) []token {
	var all []token
	z := html.NewTokenizer(bytes.NewReader(page))
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			return all
		}
		t := z.Token()
		for i, a := range t.Attr {
			if urlAttrs[a.Key] {
				if decoded, err := url.PathUnescape(a.Val); err == nil {
					t.Attr[i].Val = decoded
				}
			}
		}
		all = append(all, token{kind: t.Type, data: t.Data, attrs: t.Attr})
	}
}

// diffHTML says how two pages differ as HTML, or returns an empty string if
// they don't.
func diffHTML(a, b []byte, nameA, nameB string) string {
	tokensA, tokensB := tokens(a), tokens(b)
	for i := 0; i < len(tokensA) && i < len(tokensB); i++ {
		if !reflect.DeepEqual(tokensA[i], tokensB[i]) {
			return fmt.Sprintf("%s has %s where %s has %s", nameA, tokensA[i], nameB, tokensB[i])
		}
	}
	if len(tokensA) != len(tokensB) {
		return fmt.Sprintf("%s has %d pieces and %s has %d", nameA, len(tokensA), nameB, len(tokensB))
	}
	return ""
}

package site

import (
	"bytes"
	stdhtml "html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Descriptions of photos and albums are Markdown, rendered as CommonMark
// with one change from the standard: a newline is a line break, as it was
// on Flickr, and is not joined into the paragraph.
//
// Raw HTML is omitted, and links to javascript: and similar URLs are
// neutered, so no description can put markup of its own into a page.
var markdown = goldmark.New(
	goldmark.WithRendererOptions(html.WithHardWraps(), html.WithXHTML()),
)

// MarkdownToHTML renders a description as HTML.
func MarkdownToHTML(source string) (string, error) {
	if source == "" {
		return "", nil
	}
	var out bytes.Buffer
	if err := markdown.Convert([]byte(source), &out); err != nil {
		return "", err
	}
	return out.String(), nil
}

// MarkdownToText returns the plain text of a description, with the syntax
// gone, for places like og:description.  It is the text of the description
// and of any code in a line, with a space for each line break and after
// each paragraph and heading.
func MarkdownToText(source string) string {
	if source == "" {
		return ""
	}
	src := []byte(source)
	doc := markdown.Parser().Parse(text.NewReader(src))

	var out strings.Builder
	ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			switch node.Kind() {
			// A text block is a paragraph in a tight list, which
			// libcmark has as a paragraph.
			case ast.KindParagraph, ast.KindTextBlock, ast.KindHeading:
				out.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}

		switch n := node.(type) {
		case *ast.Text:
			out.WriteString(literal(n.Segment.Value(src), n.IsRaw()))
			if n.SoftLineBreak() || n.HardLineBreak() {
				out.WriteByte(' ')
			}
		case *ast.String:
			out.WriteString(literal(n.Value, n.IsRaw()))
		case *ast.AutoLink:
			out.Write(n.Label(src))
		case *ast.CodeSpan:
			out.WriteString(codeSpanText(n, src))
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}

// literal is the text a run of source stands for: with its backslash
// escapes and entities resolved, unless it is raw.  The parser leaves
// those in the source, for a renderer to deal with, so this has the HTML
// renderer's writer deal with them, and then takes its escaping off again.
func literal(value []byte, raw bool) string {
	if raw {
		return string(value)
	}
	var buf bytes.Buffer
	w := bufWriter{&buf}
	html.DefaultWriter.Write(w, value)
	return stdhtml.UnescapeString(buf.String())
}

// codeSpanText is the content of a code span as CommonMark defines it:
// line endings are spaces, and one space is taken from each end if there is
// one at both and the content isn't all spaces.
func codeSpanText(n *ast.CodeSpan, src []byte) string {
	var content strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			content.Write(t.Segment.Value(src))
		}
	}
	s := strings.ReplaceAll(content.String(), "\n", " ")
	if len(s) > 1 && s[0] == ' ' && s[len(s)-1] == ' ' && strings.Trim(s, " ") != "" {
		s = s[1 : len(s)-1]
	}
	return s
}

// bufWriter is a bytes.Buffer as the util.BufWriter the HTML writer wants.
type bufWriter struct{ *bytes.Buffer }

func (w bufWriter) Available() int { return w.Buffer.Available() }
func (w bufWriter) Buffered() int  { return w.Buffer.Len() }
func (w bufWriter) Flush() error   { return nil }

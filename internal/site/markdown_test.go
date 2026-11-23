package site

import "testing"

// markdownIs checks what a description is as HTML and as plain text.
func markdownIs(t *testing.T, desc, source, wantHTML, wantText string) {
	t.Helper()
	gotHTML, err := MarkdownToHTML(source)
	if err != nil {
		t.Errorf("%s: %v", desc, err)
	}
	if gotHTML != wantHTML {
		t.Errorf("%s: as HTML\n got: %q\nwant: %q", desc, gotHTML, wantHTML)
	}
	if gotText := MarkdownToText(source); gotText != wantText {
		t.Errorf("%s: as text\n got: %q\nwant: %q", desc, gotText, wantText)
	}
}

// What's wanted here is what Perl jiggle makes of each description, with
// libcmark.  The two also agree on every description in the author's
// library, some 2,400 of them.
func TestMarkdown(t *testing.T) {
	markdownIs(t, "nothing", "", "", "")
	markdownIs(t, "plain", "Just words.", "<p>Just words.</p>\n", "Just words. ")
	markdownIs(t, "a newline is a line break", "one\ntwo", "<p>one<br />\ntwo</p>\n", "one two ")
	markdownIs(t, "paragraphs", "one\n\ntwo", "<p>one</p>\n<p>two</p>\n", "one two ")
	markdownIs(t, "emphasis and a link", "*really* **quite** [good](https://example.com/a?b=1&c=2)", "<p><em>really</em> <strong>quite</strong> <a href=\"https://example.com/a?b=1&amp;c=2\">good</a></p>\n", "really quite good ")
	markdownIs(t, "an autolink", "see <https://example.com/x>", "<p>see <a href=\"https://example.com/x\">https://example.com/x</a></p>\n", "see https://example.com/x ")
	markdownIs(t, "raw HTML is omitted", "a <b>bold</b> move\n\n<script>alert(1)</script>", "<p>a <!-- raw HTML omitted -->bold<!-- raw HTML omitted --> move</p>\n<!-- raw HTML omitted -->\n", "a bold move ")
	markdownIs(t, "a javascript link is neutered", "[click](javascript:alert(1))", "<p><a href=\"\">click</a></p>\n", "click ")
	markdownIs(t, "entities and escapes", "AT&amp;T \\*not emphasis\\* &copy; 5 < 6 \"quoted\"", "<p>AT&amp;T *not emphasis* © 5 &lt; 6 &quot;quoted&quot;</p>\n", "AT&T *not emphasis* © 5 < 6 \"quoted\" ")
	markdownIs(t, "code", "run `jiggle  build` now\n\n    indented code\n", "<p>run <code>jiggle  build</code> now</p>\n<pre><code>indented code\n</code></pre>\n", "run jiggle  build now ")
	markdownIs(t, "a heading and a list", "# Title\n\n- one\n- two", "<h1>Title</h1>\n<ul>\n<li>one</li>\n<li>two</li>\n</ul>\n", "Title one two ")
	markdownIs(t, "a quote", "> said\n> twice", "<blockquote>\n<p>said<br />\ntwice</p>\n</blockquote>\n", "said twice ")
	markdownIs(t, "trailing spaces before a break", "one  \ntwo   \nthree", "<p>one<br />\ntwo<br />\nthree</p>\n", "one two three ")
	markdownIs(t, "an image", "![alt text](/img/x.jpg \"a title\")", "<p><img src=\"/img/x.jpg\" alt=\"alt text\" title=\"a title\" /></p>\n", "alt text ")
}

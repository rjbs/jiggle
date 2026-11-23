package sitediff

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// site makes a directory holding the given files, each "path=content".
func site(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, file := range files {
		rel, content, _ := strings.Cut(file, "=")
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func compare(t *testing.T, perl, golang string) *Report {
	t.Helper()
	r, err := Compare(Options{
		A: perl, B: golang, NameA: "Perl", NameB: "Go",
		Skip: []string{"pagefind/"}, Sizes: []string{"img/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fileIs checks how one file compares, given what each site has of it:
// "identical", "equivalent", or the difference reported.
func fileIs(t *testing.T, desc, rel, perl, golang, want string) {
	t.Helper()
	r := compare(t, site(t, rel+"="+perl), site(t, rel+"="+golang))

	got := "identical"
	switch {
	case len(r.Differences) > 0:
		got = strings.TrimPrefix(r.Differences[0], rel+": ")
	case len(r.Equivalent) > 0:
		got = "equivalent"
	}
	if got != want {
		t.Errorf("%s: got %q, want %q", desc, got, want)
	}
}

func TestPages(t *testing.T) {
	fileIs(t, "the same bytes", "a.html", `<p>one</p>`, `<p>one</p>`, "identical")
	fileIs(t, "a character written two ways", "a.html", `<p title="&quot;x&quot;">it&#39;s</p>`, `<p title="&#34;x&#34;">it's</p>`, "equivalent")
	fileIs(t, "space between attributes", "a.html", "<img src=\"x\"\n     alt=\"y\">", `<img src="x" alt="y">`, "equivalent")
	fileIs(t, "a URL written two ways", "a.html", `<a href="/tags/søren/">x</a>`, `<a href="/tags/s%c3%b8ren/">x</a>`, "equivalent")
	fileIs(t, "other attributes are not URLs", "a.html", `<a title="50%25">x</a>`, `<a title="50%">x</a>`,
		`Perl has <a title="50%25"> where Go has <a title="50%">`)
	fileIs(t, "different text", "a.html", `<p>one</p>`, `<p>two</p>`, `Perl has the text "one" where Go has the text "two"`)
	fileIs(t, "space in text counts", "a.html", "<p>one</p>\n", "<p>one</p>", "Perl has 4 pieces and Go has 3")
	fileIs(t, "a different attribute", "a.html", `<p class="a">x</p>`, `<p class="b">x</p>`, `Perl has <p class="a"> where Go has <p class="b">`)
	fileIs(t, "a missing tag", "a.html", `<p>x</p><hr>`, `<p>x</p>`, "Perl has 4 pieces and Go has 3")
	fileIs(t, "a script's text counts exactly", "a.html", `<script>a &amp; b</script>`, `<script>a & b</script>`,
		`Perl has the text "a &amp; b" where Go has the text "a & b"`)
}

func TestOtherFiles(t *testing.T) {
	fileIs(t, "the same JSON", "a.json", `{"a":1,"b":2}`, `{"a":1,"b":2}`, "identical")
	fileIs(t, "JSON written two ways", "a.json", `{"a":1.0,"b":2}`, `{"b": 2, "a": 1}`, "equivalent")
	fileIs(t, "different JSON", "a.json", `{"a":1}`, `{"a":2}`, "the data differs")
	fileIs(t, "GeoJSON is JSON", "a.geojson", `[40.0]`, `[40]`, "equivalent")
	fileIs(t, "anything else is its bytes", "feed.xml", `<a b="&quot;"/>`, `<a b="&#34;"/>`, "the bytes differ")
	fileIs(t, "a rendition is its size", "img/a/1024.webp", "abc", "xyz", "identical")
	fileIs(t, "a rendition of another size", "img/a/1024.webp", "abc", "abcd", "3 bytes in Perl, 4 in Go")
	fileIs(t, "the search index is left out", "pagefind/index.bin", "abc", "xyz", "identical")
}

func TestFilesInOnlyOneSite(t *testing.T) {
	r := compare(t,
		site(t, "index.html=x", "p/a/index.html=a", "pagefind/one.bin=1"),
		site(t, "index.html=x", "p/b/index.html=b", "pagefind/two.bin=2"))

	want := []string{"p/a/index.html: only in Perl", "p/b/index.html: only in Go"}
	if !reflect.DeepEqual(r.Differences, want) {
		t.Errorf("differences\n got: %q\nwant: %q", r.Differences, want)
	}
	if r.Files != 3 || r.Identical != 1 {
		t.Errorf("report: %+v", r)
	}
}

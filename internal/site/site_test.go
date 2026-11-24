package site

import (
	"strings"
	"testing"
)

func tagSlugIs(t *testing.T, tag, want string) {
	t.Helper()
	if got := TagSlug(tag); got != want {
		t.Errorf("TagSlug(%q) = %q, want %q", tag, got, want)
	}
}

func TestTagSlug(t *testing.T) {
	tagSlugIs(t, "vienna", "vienna")
	tagSlugIs(t, "high st", "high-st")
	tagSlugIs(t, "high-st", "high-st")
	tagSlugIs(t, "High  St.", "high-st")
	tagSlugIs(t, "--rock & roll--", "rock-roll")
	tagSlugIs(t, "søren", "søren")
	tagSlugIs(t, "café 2", "café-2")
	tagSlugIs(t, "東京", "東京")
	tagSlugIs(t, "!!!", "-")
	tagSlugIs(t, "", "-")
}

func instantIs(t *testing.T, datetime string, want int64, wantOK bool) {
	t.Helper()
	got, ok := instant(datetime)
	if got != want || ok != wantOK {
		t.Errorf("instant(%q) = %d, %v; want %d, %v", datetime, got, ok, want, wantOK)
	}
}

func TestInstant(t *testing.T) {
	instantIs(t, "1970-01-01T00:00:00Z", 0, true)
	instantIs(t, "1970-01-01T00:00:00", 0, true)
	instantIs(t, "2026-07-17T17:23:17+02:00", 1784301797, true)
	instantIs(t, "2026-07-17T15:23:17Z", 1784301797, true)
	instantIs(t, "2026-07-17T10:23:17-05:00", 1784301797, true)
	instantIs(t, "2026-07-17T21:08:17+05:45", 1784301797, true)
	instantIs(t, "2026-07-17T15:23:17.5Z", 1784301797, true)
	instantIs(t, "2026-02-30T12:00:00Z", 0, false)
	instantIs(t, "2026-13-01T12:00:00Z", 0, false)
	instantIs(t, "2026-07-17", 0, false)
	instantIs(t, "", 0, false)
}

func excerptIs(t *testing.T, desc, text string, max int, want string) {
	t.Helper()
	if got := Excerpt(text, max); got != want {
		t.Errorf("%s: Excerpt(%q, %d) = %q, want %q", desc, text, max, got, want)
	}
}

func TestExcerpt(t *testing.T) {
	excerptIs(t, "short enough", "a short one", 20, "a short one")
	excerptIs(t, "whitespace is tidied", "  a\n\nshort \t one ", 20, "a short one")
	excerptIs(t, "exactly as long as allowed", "abcde", 5, "abcde")
	excerptIs(t, "cut at a word", "one two three four", 12, "one two…")
	excerptIs(t, "cut just after a word", "one two three four", 9, "one two…")
	excerptIs(t, "one long word", "abcdefghij", 5, "abcd…")
	excerptIs(t, "counted in characters, not bytes", "ééééé ééééé ééééé", 13, "ééééé ééééé…")
	excerptIs(t, "nothing", "", 5, "")
}

func photoWith(taken, title string) *Photo { return &Photo{Taken: taken, Title: title} }

func displayIs(t *testing.T, desc string, p *Photo, wantDate, wantTitle string) {
	t.Helper()
	if got := DisplayDate(p); got != wantDate {
		t.Errorf("%s: the date is %q, want %q", desc, got, wantDate)
	}
	if got := DisplayTitle(p); got != wantTitle {
		t.Errorf("%s: the title is %q, want %q", desc, got, wantTitle)
	}
}

func TestDisplayDateAndTitle(t *testing.T) {
	displayIs(t, "titled", photoWith("2008-01-06T19:36:11", "legs"), "6 January 2008, 19:36", "legs")
	displayIs(t, "untitled", photoWith("2026-07-16T19:18:15+02:00", ""), "16 July 2026, 19:18", "16 July 2026, 19:18")
	displayIs(t, "untitled and undated", photoWith("", ""), "", "Untitled")
	displayIs(t, "undated", photoWith("", "legs"), "", "legs")
}

func durationIs(t *testing.T, seconds float64, want string) {
	t.Helper()
	if got := DisplayDuration(seconds); got != want {
		t.Errorf("DisplayDuration(%v) = %q, want %q", seconds, got, want)
	}
}

func TestDisplayDuration(t *testing.T) {
	durationIs(t, 7.165, "0:07")
	durationIs(t, 41.5, "0:42")
	durationIs(t, 102, "1:42")
	durationIs(t, 3723, "1:02:03")
}

func sampleIs(t *testing.T, n, of int, want string) {
	t.Helper()
	photos := make([]*Photo, of)
	for i := range photos {
		photos[i] = &Photo{ID: string(rune('a' + i))}
	}
	var got strings.Builder
	for _, p := range Sample(n, photos) {
		got.WriteString(p.ID)
	}
	if got.String() != want {
		t.Errorf("Sample(%d) of %d is %q, want %q", n, of, got.String(), want)
	}
}

func TestSample(t *testing.T) {
	sampleIs(t, 4, 3, "abc")
	sampleIs(t, 4, 4, "abcd")
	sampleIs(t, 4, 8, "aceg")
	sampleIs(t, 3, 10, "adg")
}

// locationIs checks where a site with the given zones and precision says a
// photo was taken, as "lat,lon", or "none".
func locationIs(t *testing.T, desc string, config Config, loc *Location, want string) {
	t.Helper()
	s := &Site{Config: config}
	got := "none"
	if public := s.PublicLocation(&Photo{Location: loc}); public != nil {
		got = toJSONText([]number{number(public.Lat), number(public.Lon)})
	}
	if got != want {
		t.Errorf("%s: the public location is %s, want %s", desc, got, want)
	}
}

func TestPublicLocation(t *testing.T) {
	plain := Config{LocationPrecision: 3}
	zoned := Config{LocationPrecision: 3, PrivateZones: []Zone{{Lat: 40.0, Lon: -75.0, Radius: 500}}}

	locationIs(t, "no location", plain, nil, "none")
	locationIs(t, "rounded", plain, &Location{Lat: 48.208412, Lon: 16.373129}, "[48.208,16.373]")
	locationIs(t, "rounded to a whole number", plain, &Location{Lat: 40.0001, Lon: -74.9999}, "[40.0,-75.0]")
	locationIs(t, "a private location", plain, &Location{Lat: 48.2084, Lon: 16.3731, Private: true}, "none")
	locationIs(t, "more precisely", Config{LocationPrecision: 5}, &Location{Lat: 48.208412, Lon: 16.373129}, "[48.20841,16.37313]")
	locationIs(t, "less precisely", Config{LocationPrecision: 0}, &Location{Lat: 48.208412, Lon: 16.373129}, "[48.0,16.0]")

	locationIs(t, "inside a zone", zoned, &Location{Lat: 40.001, Lon: -75.001}, "none")
	locationIs(t, "at a zone's center", zoned, &Location{Lat: 40.0, Lon: -75.0}, "none")
	locationIs(t, "outside a zone", zoned, &Location{Lat: 40.01, Lon: -75.01}, "[40.01,-75.01]")

	// Just outside, at about 510 meters, though it rounds to a point
	// inside: the true location decides.
	locationIs(t, "just outside a zone", zoned, &Location{Lat: 40.00459, Lon: -75.0}, "[40.005,-75.0]")
}

func TestFeedXMLFormat(t *testing.T) {
	feed := &element{name: "feed", attrs: []string{"xmlns", "http://www.w3.org/2005/Atom"}}
	feed.text("title", `Tom & Jerry's <"photos">`)
	feed.empty("link", "href", `https://example.com/?a=1&b="2"`, "rel", "self")
	feed.child("author").text("name", "Some Body")
	feed.text("content", "<p>one</p>\n<p>two</p>\n", "type", "html")
	feed.text("title", "")

	var out strings.Builder
	feed.write(&out, 0)

	want := `<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Tom &amp; Jerry's &lt;"photos"&gt;</title>
  <link href="https://example.com/?a=1&amp;b=&quot;2&quot;" rel="self"/>
  <author>
    <name>Some Body</name>
  </author>
  <content type="html">&lt;p&gt;one&lt;/p&gt;
&lt;p&gt;two&lt;/p&gt;
</content>
  <title/>
</feed>
`
	if out.String() != want {
		t.Errorf("the feed is written as\n%s\nwant\n%s", out.String(), want)
	}
}

func jsonIs(t *testing.T, desc string, v any, want string) {
	t.Helper()
	if got := toJSONText(v); got != want {
		t.Errorf("%s: got %s, want %s", desc, got, want)
	}
}

func TestJSON(t *testing.T) {
	jsonIs(t, "keys in order, nothing escaped that needn't be",
		map[string]any{"b": "<a href=\"x\">R&D</a>", "a": []int{1, 2}, "c": nil},
		`{"a":[1,2],"b":"<a href=\"x\">R&D</a>","c":null}`)
	separator := string(rune(0x2028))
	jsonIs(t, "characters as themselves", "café "+separator+" \U0001F9B5", "\"café "+separator+" \U0001F9B5\"")
	jsonIs(t, "media, with its keys in order", &media{URL: "/x", Width: 4, Height: 3}, `{"height":3,"url":"/x","width":4}`)
	jsonIs(t, "a whole number that isn't one by nature", number(40), `40.0`)
	jsonIs(t, "a negative one", number(-75), `-75.0`)
	jsonIs(t, "a fraction", number(-75.373), `-75.373`)
}

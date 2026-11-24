package site

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// feedSize is how many entries the feed has, at most.
const feedSize = 30

// FeedEntry is one entry of the feed: an album, a day's photos, or a photo.
type FeedEntry struct {
	// Kind is "album", "day", or "photo".
	Kind string

	// When is the entry's date, a datetime.
	When string

	Album *Album
	Photo *Photo
	Day   *FeedDay
}

// FeedDay is the listed photos, in no listed album, of one day: the day
// they were taken, or, for photos with no date taken, the day they were
// added.
type FeedDay struct {
	// By is "taken" or "added".
	By string

	// Date is the day, like "2026-01-22".
	Date   string
	Photos []*Photo
}

var dateRE = regexp.MustCompile(`\A(\d{4}-\d\d-\d\d)`)

// FeedEntries returns the newest entries for the site's feed, newest
// first.  An entry is a listed album, which stands for all of its photos,
// or the listed photos in no listed album that were taken on one day.
// (Photos with no taken date go by the day they were added.)  A day with
// just one such photo is an entry for that photo.
//
// An album is dated by when it was created, and a day or a photo by the
// most recent addition to it.  Anything without a date is left out.
func (s *Site) FeedEntries() []*FeedEntry {
	var entries []*FeedEntry

	for _, a := range s.ListedAlbums {
		// An album with no creation date is dated by its newest photo.
		when := a.Created
		if when == "" {
			for _, p := range a.Photos {
				when = max(when, p.AddedAt())
			}
		}
		if when != "" {
			entries = append(entries, &FeedEntry{Kind: "album", When: when, Album: a})
		}
	}

	// Photos in no album are grouped by the day they were taken, or, if
	// they have no taken date, the day they were added, so a week of
	// photos makes a week of entries, not one per photo.  A day's entry
	// is dated by the newest addition to it, so the feed stays "what's
	// new".
	inAlbum := s.inListedAlbum()
	days := map[string]*FeedDay{}
	var keys []string
	for _, p := range s.Listed {
		if inAlbum[p.ID] || p.AddedAt() == "" {
			continue
		}
		by, of := "taken", p.Taken
		if of == "" {
			by, of = "added", p.AddedAt()
		}
		m := dateRE.FindStringSubmatch(of)
		if m == nil {
			continue
		}
		key := by + "-" + m[1]
		if days[key] == nil {
			days[key] = &FeedDay{By: by, Date: m[1]}
			keys = append(keys, key)
		}
		days[key].Photos = append(days[key].Photos, p)
	}
	sort.Strings(keys)

	for _, key := range keys {
		day := days[key]
		sort.SliceStable(day.Photos, func(i, j int) bool {
			a, b := day.Photos[i], day.Photos[j]
			if a.Taken != b.Taken {
				return a.Taken < b.Taken
			}
			return a.ID < b.ID
		})

		when, newest := "", int64(math.MinInt64)
		for _, p := range day.Photos {
			at, _ := instant(p.AddedAt())
			if at > newest {
				when, newest = p.AddedAt(), at
			}
		}

		if len(day.Photos) == 1 {
			entries = append(entries, &FeedEntry{Kind: "photo", When: when, Photo: day.Photos[0]})
		} else {
			entries = append(entries, &FeedEntry{Kind: "day", When: when, Day: day})
		}
	}

	dated := entries[:0]
	for _, e := range entries {
		if _, ok := instant(e.When); ok {
			dated = append(dated, e)
		}
	}
	sort.SliceStable(dated, func(i, j int) bool {
		a, _ := instant(dated[i].When)
		b, _ := instant(dated[j].When)
		if a != b {
			return a > b
		}
		return dated[i].Kind < dated[j].Kind
	})

	if len(dated) > feedSize {
		dated = dated[:feedSize]
	}
	return dated
}

// htmlEscaper escapes text for the HTML of a feed entry.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

// thumbs is a row of small images of photos, each a link to its photo, all
// of one height.
func (s *Site) thumbs(photos []*Photo, height int) string {
	var links []string
	for _, p := range photos {
		thumb := p.Renditions["h480.webp"]
		width := int(float64(height)*float64(thumb.Width)/float64(thumb.Height) + 0.5)
		links = append(links, fmt.Sprintf(`<a href="%s"><img src="%s" height="%d" width="%d" alt="%s"></a>`,
			s.Absolute(PhotoURL(p)), s.Absolute(RenditionURL(p, "h480.webp")), height, width,
			htmlEscaper.Replace(DisplayTitle(p))))
	}
	return "<p>" + strings.Join(links, " ") + "</p>\n"
}

func (s *Site) feedPhotoHTML(p *Photo) (string, error) {
	image := p.Renditions["1024.webp"]
	url := s.Absolute(PhotoURL(p))

	html := fmt.Sprintf("<p><a href=\"%s\"><img src=\"%s\" width=\"%d\" height=\"%d\" alt=\"%s\"></a></p>\n",
		url, s.Absolute(RenditionURL(p, "1024.webp")), image.Width, image.Height,
		htmlEscaper.Replace(DisplayTitle(p)))
	if p.IsVideo() {
		html += fmt.Sprintf("<p><a href=\"%s\">Play the video</a></p>\n", url)
	}

	description, err := MarkdownToHTML(p.Description)
	if err != nil {
		return "", err
	}
	html += description

	if len(p.Tags) > 0 {
		var links []string
		for _, tag := range p.Tags {
			links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
				s.Absolute(TagURL(TagSlug(tag))), htmlEscaper.Replace(tag)))
		}
		html += "<p>Tags: " + strings.Join(links, ", ") + "</p>\n"
	}
	return html, nil
}

func (s *Site) feedAlbumHTML(a *Album) (string, error) {
	url := s.Absolute(AlbumURL(a))
	cover := a.Cover.Renditions["1024.webp"]

	html := fmt.Sprintf("<p><a href=\"%s\"><img src=\"%s\" width=\"%d\" height=\"%d\" alt=\"\"></a></p>\n",
		url, s.Absolute(RenditionURL(a.Cover, "1024.webp")), cover.Width, cover.Height)

	description, err := MarkdownToHTML(a.Description)
	if err != nil {
		return "", err
	}
	html += description

	// The feed is a listing, so its samples link only to listed photos;
	// the cover, which may be unlisted, links to the album.
	var listed, more []*Photo
	for _, p := range a.Photos {
		if p.IsListed() {
			listed = append(listed, p)
		}
	}
	for _, p := range Sample(8, listed) {
		if p.ID != a.Cover.ID {
			more = append(more, p)
		}
	}
	if len(more) > 0 {
		html += s.thumbs(more, 120)
	}

	html += fmt.Sprintf("<p><a href=\"%s\">%s</a></p>\n", url, Count(len(a.Photos), "photo"))
	return html, nil
}

func (s *Site) feedDayHTML(day *FeedDay, url string) string {
	shown := day.Photos
	if len(shown) > 12 {
		shown = shown[:12]
	}

	how := "added"
	if day.By == "taken" {
		how = "from"
	}
	return s.thumbs(shown, 160) + fmt.Sprintf("<p><a href=\"%s\">%d photos %s %s</a></p>\n",
		url, len(day.Photos), how, DisplayDay(day.Date))
}

var hostRE = regexp.MustCompile(`\A\w+://([^/:]+)`)

// FeedXML returns the site's Atom feed.  See FeedEntries.
func (s *Site) FeedXML() ([]byte, error) {
	entries := s.FeedEntries()
	home := s.Absolute("/")

	feed := &element{name: "feed", attrs: []string{"xmlns", "http://www.w3.org/2005/Atom"}}
	feed.text("title", s.Config.Title)
	feed.text("id", home)
	feed.empty("link", "href", home, "rel", "alternate", "type", "text/html")
	feed.empty("link", "href", s.Absolute("/feed.xml"), "rel", "self", "type", "application/atom+xml")
	updated := "1970-01-01T00:00:00Z"
	if len(entries) > 0 {
		updated = entries[0].When
	}
	feed.text("updated", updated)
	feed.child("author").text("name", s.Config.Author)

	for _, e := range entries {
		var title, url, id, html string
		var tags []string
		var err error

		switch e.Kind {
		case "album":
			title, url = e.Album.Title, s.Absolute(AlbumURL(e.Album))
			html, err = s.feedAlbumHTML(e.Album)

		case "day":
			day := e.Day
			title = fmt.Sprintf("%s, and %d more", DisplayTitle(day.Photos[0]), len(day.Photos)-1)
			url = s.Absolute("/archive/undated/")
			if day.By == "taken" {
				url = s.Absolute(MonthURL(day.Date[0:4], day.Date[5:7]))
			}
			html = s.feedDayHTML(day, url)

			seen := map[string]bool{}
			for _, p := range day.Photos {
				for _, tag := range p.Tags {
					if !seen[tag] {
						seen[tag] = true
						tags = append(tags, tag)
					}
				}
			}
			sort.Strings(tags)

			// Many days share a month page, so the entry's id can't be
			// its URL.  A tag URI names the day itself, so it stays the
			// same when the day gets more photos later.
			host := "localhost"
			if m := hostRE.FindStringSubmatch(s.Config.BaseURL); m != nil {
				host = m[1]
			}
			id = fmt.Sprintf("tag:%s,2026:day/%s/%s", host, day.By, day.Date)

		default:
			title, url = DisplayTitle(e.Photo), s.Absolute(PhotoURL(e.Photo))
			html, err = s.feedPhotoHTML(e.Photo)
			tags = e.Photo.Tags
		}
		if err != nil {
			return nil, err
		}
		if id == "" {
			id = url
		}

		entry := feed.child("entry")
		entry.text("title", title)
		entry.text("id", id)
		entry.empty("link", "href", url, "rel", "alternate", "type", "text/html")
		entry.text("published", e.When)
		entry.text("updated", e.When)
		for _, tag := range tags {
			entry.empty("category", "term", tag)
		}
		entry.text("content", html, "type", "html")
	}

	var out strings.Builder
	out.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	feed.write(&out, 0)
	return []byte(out.String()), nil
}

// element is an XML element, which has either text or child elements.  It
// is written as libxml2 writes a formatted document, which is how Perl
// jiggle writes the feed, so that a feed that hasn't changed is the same
// bytes.
type element struct {
	name string

	// attrs is the attributes' names and values, by turns, in order of
	// name.
	attrs    []string
	content  string
	hasText  bool
	children []*element
}

func (e *element) child(name string, attrs ...string) *element {
	c := &element{name: name, attrs: attrs}
	e.children = append(e.children, c)
	return c
}

func (e *element) text(name, content string, attrs ...string) {
	c := e.child(name, attrs...)
	c.content, c.hasText = content, content != ""
}

func (e *element) empty(name string, attrs ...string) { e.child(name, attrs...) }

var (
	xmlText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#13;")
	xmlAttr = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
		"\r", "&#13;", "\n", "&#10;", "\t", "&#9;")
)

func (e *element) write(out *strings.Builder, depth int) {
	indent := strings.Repeat("  ", depth)
	out.WriteString(indent + "<" + e.name)
	for i := 0; i < len(e.attrs); i += 2 {
		out.WriteString(" " + e.attrs[i] + `="` + xmlAttr.Replace(e.attrs[i+1]) + `"`)
	}

	switch {
	case e.hasText:
		out.WriteString(">" + xmlText.Replace(e.content) + "</" + e.name + ">\n")
	case len(e.children) > 0:
		out.WriteString(">\n")
		for _, c := range e.children {
			c.write(out, depth+1)
		}
		out.WriteString(indent + "</" + e.name + ">\n")
	default:
		out.WriteString("/>\n")
	}
}

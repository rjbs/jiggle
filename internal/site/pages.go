package site

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"path"
	"strconv"
	"strings"
)

//go:embed templates/*.html
var templateFiles embed.FS

// The templates are html/template's, which escapes each value for the place
// it's put.  Each is laid out so that its output has the line breaks and
// indentation of the Mojo template it was ported from: a line that is only
// an action begins with "{{-", which takes away the line break before it,
// and leaves the one after it to end the line before.
var templates = template.Must(parseTemplates())

func parseTemplates() (*template.Template, error) {
	set := template.New("").Funcs(template.FuncMap{"attr": attr})
	files, err := templateFiles.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		text, err := templateFiles.ReadFile(path.Join("templates", file.Name()))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(file.Name(), ".html")
		if _, err := set.New(name).Parse(string(text)); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// attr makes an attribute, escaping in its value only what has to be: &, <,
// >, and ".  The templates escape apostrophes too, which is correct, but
// some programs read attributes without decoding entities: Feedbin, finding
// the feed through its discovery link, named it "rjbs&#39;s photos".
func attr(name, value string) template.HTMLAttr {
	return template.HTMLAttr(name + `="` + attrEscaper.Replace(value) + `"`)
}

var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func render(name string, data any) (string, error) {
	var out bytes.Buffer
	if err := templates.ExecuteTemplate(&out, name, data); err != nil {
		return "", fmt.Errorf("rendering %s: %w", name, err)
	}
	return out.String(), nil
}

// page is what the layout needs of a page, besides its content.
type page struct {
	Title     string
	OG        *openGraph
	NoIndex   bool
	Canonical string

	// Map and Viewer say whether the page needs the scripts for a map or
	// for the full-screen viewer.
	Map    bool
	Viewer bool
}

// openGraph is how a page describes itself to whatever makes a preview of
// a link to it.
type openGraph struct {
	Title       string
	URL         string
	Image       string
	Width       int64
	Height      int64
	Description string
	Video       *media
}

// media is the URL and size of an image or a video.  Its fields are in the
// order of their names in JSON, which is the order Perl jiggle writes them
// in.
type media struct {
	Height int64  `json:"height"`
	URL    string `json:"url"`
	Width  int64  `json:"width"`
}

// renderPage renders a template as the content of a whole page.
func (s *Site) renderPage(name string, p page, data any) ([]byte, error) {
	content, err := render(name, data)
	if err != nil {
		return nil, err
	}

	title := p.Title
	if title != s.Config.Title {
		title += " — " + s.Config.Title
	}
	html, err := render("layout", struct {
		page
		PageTitle string
		SiteTitle string
		Content   template.HTML
	}{p, title, s.Config.Title, template.HTML(content)})
	return []byte(entities.Replace(html)), err
}

// entities makes two of html/template's escapes the ones Perl jiggle's
// templates write, so that a page that hasn't changed is the same bytes: a
// quotation mark as &quot;, and a plus sign, which html/template escapes in
// attributes, as itself.  Neither sequence can be in a page any other way:
// in text or an attribute, a "&#34;" somebody typed has its ampersand
// escaped, and in a script html/template writes no entities at all.
// -- claude, 2026-11-22
var entities = strings.NewReplacer("&#34;", "&quot;", "&#43;", "+")

// photoOpenGraph is the OpenGraph data for a photo's own page.
func (s *Site) photoOpenGraph(p *Photo) *openGraph {
	og := s.pageOpenGraph(DisplayTitle(p), PhotoURL(p), p, Excerpt(MarkdownToText(p.Description), 200))
	if p.IsVideo() {
		video := p.Renditions["video.mp4"]
		og.Video = mediaOf(s.Absolute(RenditionURL(p, "video.mp4")), video)
	}
	return og
}

// pageOpenGraph returns the OpenGraph data for a page that lists photos,
// like an album or the home page: its title and URL, a photo's preview
// image to stand for it, and a description.  It's nil if there's no photo
// to show.
func (s *Site) pageOpenGraph(title, path string, p *Photo, description string) *openGraph {
	if p == nil {
		return nil
	}
	image := p.Renditions["og.jpg"]
	return &openGraph{
		Title:       title,
		URL:         s.Absolute(path),
		Image:       s.Absolute(RenditionURL(p, "og.jpg")),
		Width:       image.Width,
		Height:      image.Height,
		Description: description,
	}
}

// tile is one item of a grid.
type tile struct {
	Class   string
	AR      string
	URL     string
	Image   string
	Width   int64
	Height  int64
	Name    string
	Details string
}

func aspectRatio(width, height int64) string {
	return strconv.FormatFloat(float64(width)/float64(height), 'f', 4, 64)
}

// tiles makes a grid of items.  With an album, the grid is that album's,
// and photos link to their pages within it.
func (s *Site) tiles(items []HomeItem, in *Album) []tile {
	tiles := make([]tile, len(items))
	for i, item := range items {
		shown := item.Photo
		t := tile{}
		switch {
		case item.Album != nil:
			shown = item.Album.Cover
			t.Class = "album"
			t.URL = AlbumURL(item.Album)
		case in != nil:
			t.URL = AlbumPhotoURL(in, shown)
		default:
			t.URL = PhotoURL(shown)
		}
		if item.Album == nil && shown.IsVideo() {
			t.Class = "video"
		}

		thumb := shown.Renditions["h480.webp"]
		t.AR = aspectRatio(thumb.Width, thumb.Height)
		t.Image = RenditionURL(shown, "h480.webp")
		t.Width, t.Height = thumb.Width, thumb.Height
		t.Name, t.Details = s.GridCaption(item)
		tiles[i] = t
	}
	return tiles
}

func (s *Site) photoTiles(photos []*Photo, in *Album) []tile {
	items := make([]HomeItem, len(photos))
	for i, p := range photos {
		items[i] = HomeItem{Photo: p}
	}
	return s.tiles(items, in)
}

// link is a link, as its URL and what it says.
type link struct {
	URL   string
	Label string
}

// inAlbum is where a photo's page is in an album, for a page that is the
// photo's within that album.
type inAlbum struct {
	Album    *Album
	Position int
}

// photoPage renders a photo's page: its own, or, with in, its page within
// an album, whose neighbors are its neighbors there.
func (s *Site) photoPage(p *Photo, prev, next *link, in *inAlbum) ([]byte, error) {
	large := p.Renditions["2048.webp"]
	location := s.PublicLocation(p)

	data := struct {
		Indexed bool
		Thumb   string
		Video   *struct {
			media
			Poster, AR string
		}
		Image       string
		Srcset      string
		Width       int64
		Height      int64
		AR          string
		Title       string
		Description template.HTML
		Prev, Next  *link
		InAlbum     *struct {
			link
			Title    string
			Position int
			Count    int
			List     string
			Entry    string
		}
		Taken    string
		MonthURL string
		Albums   []link
		Tags     []link
		Location *struct{ Lat, Lon template.JS }
	}{
		Indexed: p.IsListed() && in == nil,
		Thumb:   RenditionURL(p, "h480.webp"),
		Image:   RenditionURL(p, "1024.webp"),
		Srcset:  Srcset(p),
		Width:   large.Width,
		Height:  large.Height,
		AR:      aspectRatio(large.Width, large.Height),
		Title:   DisplayTitle(p),
		Prev:    prev,
		Next:    next,
		Taken:   DisplayDate(p),
	}

	description, err := MarkdownToHTML(p.Description)
	if err != nil {
		return nil, err
	}
	data.Description = template.HTML(description)

	if p.IsVideo() {
		video := p.Renditions["video.mp4"]
		data.Video = &struct {
			media
			Poster, AR string
		}{
			*mediaOf(RenditionURL(p, "video.mp4"), video),
			RenditionURL(p, "2048.webp"),
			aspectRatio(video.Width, video.Height),
		}
	}
	if year, month := MonthOf(p); year != "" {
		data.MonthURL = MonthURL(year, month)
	}
	for _, a := range s.AlbumsFor(p) {
		data.Albums = append(data.Albums, link{AlbumURL(a), a.Title})
	}
	for _, tag := range p.Tags {
		data.Tags = append(data.Tags, link{TagURL(TagSlug(tag)), tag})
	}
	if location != nil {
		number := func(v float64) template.JS { return template.JS(strconv.FormatFloat(v, 'f', -1, 64)) }
		data.Location = &struct{ Lat, Lon template.JS }{number(location.Lat), number(location.Lon)}
	}

	meta := page{
		Title:   DisplayTitle(p),
		OG:      s.photoOpenGraph(p),
		NoIndex: !p.IsListed(),
		Map:     location != nil,
	}
	if in != nil {
		a := in.Album
		data.InAlbum = &struct {
			link
			Title    string
			Position int
			Count    int
			List     string
			Entry    string
		}{link{URL: AlbumURL(a)}, a.Title, in.Position, len(a.Photos), albumListURL(a), toJSONText(s.viewerEntry(a, p))}

		// These are copies of the photo's own page, so they name it as
		// canonical.
		meta.NoIndex = !(p.IsListed() && a.Listed)
		meta.Canonical = s.Absolute(PhotoURL(p))
		meta.Viewer = true
	}
	return s.renderPage("photo", meta, data)
}

// albumListURL is where an album's list for the full-screen viewer is.
func albumListURL(a *Album) string { return "/albums/" + a.Slug + "/photos.json" }

func (s *Site) homePage(items []HomeItem) ([]byte, error) {
	meta := page{Title: s.Config.Title}
	if len(items) > 0 {
		lead := items[0]
		shown := lead.Photo
		if lead.Album != nil {
			shown = lead.Album.Cover
		}
		name, _ := s.GridCaption(lead)
		meta.OG = s.pageOpenGraph(s.Config.Title, "/", shown, "Recently added: "+name)
	}
	return s.renderPage("index", meta, struct{ Tiles []tile }{s.tiles(items, nil)})
}

func (s *Site) albumsPage() ([]byte, error) {
	type entry struct {
		URL, Image, Title, Count string
	}
	var entries []entry
	for _, a := range s.ListedAlbums {
		entries = append(entries, entry{
			AlbumURL(a), RenditionURL(a.Cover, "h480.webp"), a.Title, Count(len(a.Photos), "photo"),
		})
	}

	meta := page{Title: "Albums"}
	if len(s.ListedAlbums) > 0 {
		meta.OG = s.pageOpenGraph("Albums", "/albums/", s.ListedAlbums[0].Cover, Count(len(s.ListedAlbums), "album"))
	}
	return s.renderPage("albums", meta, struct{ Albums []entry }{entries})
}

func (s *Site) albumPage(a *Album) ([]byte, error) {
	description, err := MarkdownToHTML(a.Description)
	if err != nil {
		return nil, err
	}

	summary := Excerpt(MarkdownToText(a.Description), 200)
	if summary == "" {
		summary = Count(len(a.Photos), "photo")
	}
	meta := page{
		Title:   a.Title,
		OG:      s.pageOpenGraph(a.Title, AlbumURL(a), a.Cover, summary),
		NoIndex: !a.Listed,
		Viewer:  true,
	}
	return s.renderPage("album", meta, struct {
		Title       string
		List        string
		First       string
		Description template.HTML
		Tiles       []tile
	}{a.Title, albumListURL(a), toJSONText(s.viewerEntry(a, a.Photos[0])), template.HTML(description), s.photoTiles(a.Photos, a)})
}

func (s *Site) tagsPage() ([]byte, error) {
	type entry struct {
		URL, Name string
		Count     int
	}
	var entries []entry
	for _, tag := range s.Tags {
		entries = append(entries, entry{TagURL(tag.Slug), tag.Name, len(tag.Photos)})
	}
	meta := page{Title: "Tags", OG: s.pageOpenGraph("Tags", "/tags/", first(s.Listed), Count(len(s.Tags), "tag"))}
	return s.renderPage("tags", meta, struct{ Tags []entry }{entries})
}

func (s *Site) tagPage(tag *Tag) ([]byte, error) {
	meta := page{
		Title: tag.Name,
		OG: s.pageOpenGraph(tag.Name, TagURL(tag.Slug), tag.Photos[0],
			Count(len(tag.Photos), "photo")+" tagged "+tag.Name),
	}
	return s.renderPage("tag", meta, struct {
		Name  string
		Tiles []tile
	}{tag.Name, s.photoTiles(tag.Photos, nil)})
}

func first(photos []*Photo) *Photo {
	if len(photos) == 0 {
		return nil
	}
	return photos[0]
}

func (s *Site) archivePage() ([]byte, error) {
	type year struct {
		URL, Year, Count string
		Tiles            []tile
	}
	var years []year
	for _, y := range s.Years {
		// The months are newest first, and the sample is of the year
		// from its start.
		var photos []*Photo
		for i := len(y.Months) - 1; i >= 0; i-- {
			photos = append(photos, y.Months[i].Photos...)
		}
		years = append(years, year{YearURL(y.Year), y.Year, Count(y.Count, "photo"), s.photoTiles(Sample(8, photos), nil)})
	}

	meta := page{
		Title: "Archive",
		OG:    s.pageOpenGraph("Archive", "/archive/", first(s.Listed), Count(len(s.Listed), "photo")),
	}
	return s.renderPage("archive", meta, struct {
		Years   []year
		Undated int
	}{years, len(s.Undated)})
}

func (s *Site) undatedPage() ([]byte, error) {
	return s.renderPage("undated", page{Title: "Undated"}, struct{ Tiles []tile }{s.photoTiles(s.Undated, nil)})
}

// neighbors is the links from a year or a month to the ones on either side
// of it.
type neighbors struct {
	Earlier, Later *link
}

// yearPage renders a year's page.  Years are newest first, so the later
// neighbor of each is the one before it in the list.
func (s *Site) yearPage(i int) ([]byte, error) {
	y := s.Years[i]

	type month struct {
		URL, Name, Count, More string
		Tiles                  []tile
	}
	var months []month
	for j := len(y.Months) - 1; j >= 0; j-- {
		m := y.Months[j]
		entry := month{
			URL:   MonthURL(m.Year, m.Month),
			Name:  MonthName(m.Month),
			Count: Count(len(m.Photos), "photo"),
			Tiles: s.photoTiles(Sample(12, m.Photos), nil),
		}
		if len(m.Photos) > 12 {
			entry.More = fmt.Sprintf("All %d from %s", len(m.Photos), entry.Name)
		}
		months = append(months, entry)
	}

	var near neighbors
	if i+1 < len(s.Years) {
		near.Earlier = &link{YearURL(s.Years[i+1].Year), s.Years[i+1].Year}
	}
	if i > 0 {
		near.Later = &link{YearURL(s.Years[i-1].Year), s.Years[i-1].Year}
	}

	newest := y.Months[0].Photos
	meta := page{
		Title: y.Year,
		OG:    s.pageOpenGraph(y.Year, YearURL(y.Year), newest[len(newest)-1], Count(y.Count, "photo")),
	}
	return s.renderPage("year", meta, struct {
		neighbors
		Year, Count string
		Months      []month
	}{near, y.Year, Count(y.Count, "photo"), months})
}

func monthTitle(m *Month) string { return MonthName(m.Month) + " " + m.Year }

// monthPage renders a month's page, given every month, newest first.
func (s *Site) monthPage(months []*Month, i int) ([]byte, error) {
	m := months[i]

	var near neighbors
	if i+1 < len(months) {
		near.Earlier = &link{MonthURL(months[i+1].Year, months[i+1].Month), monthTitle(months[i+1])}
	}
	if i > 0 {
		near.Later = &link{MonthURL(months[i-1].Year, months[i-1].Month), monthTitle(months[i-1])}
	}

	meta := page{
		Title: monthTitle(m),
		OG: s.pageOpenGraph(monthTitle(m), MonthURL(m.Year, m.Month), m.Photos[len(m.Photos)-1],
			Count(len(m.Photos), "photo")),
	}
	return s.renderPage("month", meta, struct {
		neighbors
		Name, Year, YearURL, Count string
		Tiles                      []tile
	}{near, MonthName(m.Month), m.Year, YearURL(m.Year), Count(len(m.Photos), "photo"), s.photoTiles(m.Photos, nil)})
}

func (s *Site) plainPage(name, title string, hasMap bool) ([]byte, error) {
	return s.renderPage(name, page{Title: title, Map: hasMap}, nil)
}

// mediaOf is a rendition as media, at the given URL.
func mediaOf(url string, r *Rendition) *media {
	return &media{URL: url, Width: r.Width, Height: r.Height}
}

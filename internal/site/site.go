// Package site renders a library into a static website.
//
// The whole site is rendered on every build.  That's cheap for HTML, and
// the expensive part, the renditions, is done ahead of time and only
// hardlinked here.  The Writer makes sure that files whose content didn't
// change aren't touched, so an incremental sync stays small.
//
// Private and pending photos are treated as though they don't exist.
// They're dropped when the photo list is first assembled, so no page,
// count, cover, map point, or feed can mention one.
//
// Unlisted photos have pages, but only albums link to them.  There are two
// lists of photos: the published ones, every photo with a page, and the
// listed ones, which everything that lists photos (the home page, the feed,
// tags, the archive, the map, search, and the links between neighbors)
// uses.  Albums are made from the published photos, and an album of nothing
// but unlisted photos is itself unlisted.
//
// This is a port of Perl jiggle's Jiggle::Site, and until that is retired
// the two are meant to build the same site from the same library.
package site

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// Site is a library as the site shows it.
type Site struct {
	Config Config

	// Published is every photo with a page, newest first.  Photos with no
	// date sort last, by id, so the order is at least stable.
	Published []*Photo

	// Listed is the photos the site lists, in the same order.
	Listed []*Photo

	// Unrendered is the ids of photos left out because they don't have
	// all their renditions, like private ones, so that nothing links to
	// files that aren't there.
	Unrendered []string

	// Albums is every album with a published photo, newest first; albums
	// with no creation date go last, by title.
	Albums []*Album

	// ListedAlbums is the albums the site lists: every one with a listed
	// photo.
	ListedAlbums []*Album

	// Tags is the tags of the listed photos, by slug.
	Tags []*Tag

	// Years is the listed photos grouped by the date they were taken.
	// Years are newest first, and so are the months within each year.
	// Each month's photos are oldest first, so that a trip reads in order.
	//
	// Photos are filed by the date on the local clock where they were
	// taken, which is how a person remembers them: a photo taken at 23:30
	// on 31 July in Vienna belongs to July, even though it was already
	// August in UTC.
	Years []*Year

	// Undated is the listed photos with no date taken.
	Undated []*Photo

	albumsFor map[string][]*Album
}

// Album is an album as the site shows it: only its published photos.
type Album struct {
	Slug        string
	Title       string
	Description string
	Created     string

	// Cover is the photo that stands for the album: the one it names, if
	// that's published, and otherwise its first.
	Cover  *Photo
	Photos []*Photo
	Listed bool
}

// Tag is a tag and the listed photos that have it.  Tags are compared, and
// named in URLs, by slug.
type Tag struct {
	Slug   string
	Name   string
	Photos []*Photo
}

type Year struct {
	Year   string
	Count  int
	Months []*Month
}

type Month struct {
	Year, Month string
	Photos      []*Photo
}

// NewSite works out what the site shows of a library.  With present, a
// photo is published only if that says its renditions are there; it is for
// checking the files themselves.
func NewSite(lib *Library, present func(*Photo) bool) *Site {
	s := &Site{Config: lib.Config}

	for _, p := range lib.Photos {
		if !p.IsPublished() || !p.HasOriginal {
			continue
		}
		if !p.Rendered() || (present != nil && !present(p)) {
			s.Unrendered = append(s.Unrendered, p.ID)
			continue
		}
		s.Published = append(s.Published, p)
	}
	sort.Strings(s.Unrendered)

	sort.SliceStable(s.Published, func(i, j int) bool {
		a, b := s.Published[i], s.Published[j]
		if (a.Taken != "") != (b.Taken != "") {
			return a.Taken != ""
		}
		if a.Taken != b.Taken {
			return a.Taken > b.Taken
		}
		return a.ID < b.ID
	})

	byID := make(map[string]*Photo, len(s.Published))
	for _, p := range s.Published {
		byID[p.ID] = p
		if p.IsListed() {
			s.Listed = append(s.Listed, p)
		}
	}

	s.buildAlbums(lib.Albums, byID)
	s.buildTags()
	s.buildArchive()
	return s
}

func (s *Site) buildAlbums(records []*AlbumRecord, byID map[string]*Photo) {
	for _, r := range records {
		a := &Album{Slug: r.Slug, Title: r.Title, Description: r.Description, Created: r.Created}
		for _, id := range r.PhotoIDs {
			if p := byID[id]; p != nil {
				a.Photos = append(a.Photos, p)
				a.Listed = a.Listed || p.IsListed()
			}
		}
		if len(a.Photos) == 0 {
			continue
		}

		// The cover might be unset, or might name a private photo.
		a.Cover = byID[r.Cover]
		if a.Cover == nil {
			a.Cover = a.Photos[0]
		}
		s.Albums = append(s.Albums, a)
	}

	// The dates may carry different offsets, so compare them as instants.
	fold := cases.Fold()
	sort.SliceStable(s.Albums, func(i, j int) bool {
		a, b := s.Albums[i], s.Albums[j]
		ai, aok := instant(a.Created)
		bi, bok := instant(b.Created)
		if aok != bok {
			return aok
		}
		if ai != bi {
			return ai > bi
		}
		return fold.String(a.Title) < fold.String(b.Title)
	})

	s.albumsFor = map[string][]*Album{}
	for _, a := range s.Albums {
		if a.Listed {
			s.ListedAlbums = append(s.ListedAlbums, a)
		}
		for _, p := range a.Photos {
			s.albumsFor[p.ID] = append(s.albumsFor[p.ID], a)
		}
	}
}

// AlbumsFor returns the albums a photo is in.
func (s *Site) AlbumsFor(p *Photo) []*Album { return s.albumsFor[p.ID] }

func (s *Site) buildTags() {
	bySlug := map[string]*Tag{}
	for _, p := range s.Listed {
		for _, name := range p.Tags {
			slug := TagSlug(name)
			if bySlug[slug] == nil {
				bySlug[slug] = &Tag{Slug: slug, Name: name}
				s.Tags = append(s.Tags, bySlug[slug])
			}
			bySlug[slug].Photos = append(bySlug[slug].Photos, p)
		}
	}
	sort.Slice(s.Tags, func(i, j int) bool { return s.Tags[i].Slug < s.Tags[j].Slug })
}

var yearMonthRE = regexp.MustCompile(`\A(\d{4})-(\d\d)-`)

// MonthOf is the archive month a photo belongs to: its year and month, or
// two empty strings.
func MonthOf(p *Photo) (year, month string) {
	if m := yearMonthRE.FindStringSubmatch(p.Taken); m != nil {
		return m[1], m[2]
	}
	return "", ""
}

func (s *Site) buildArchive() {
	months := map[string]*Month{}
	years := map[string]*Year{}

	// The listed photos are newest first, so going through them backward
	// gives oldest first within each month.
	for i := len(s.Listed) - 1; i >= 0; i-- {
		p := s.Listed[i]
		y, m := MonthOf(p)
		if y == "" {
			continue
		}
		if years[y] == nil {
			years[y] = &Year{Year: y}
			s.Years = append(s.Years, years[y])
		}
		if months[y+m] == nil {
			months[y+m] = &Month{Year: y, Month: m}
			years[y].Months = append(years[y].Months, months[y+m])
		}
		months[y+m].Photos = append(months[y+m].Photos, p)
		years[y].Count++
	}
	for _, p := range s.Listed {
		if y, _ := MonthOf(p); y == "" {
			s.Undated = append(s.Undated, p)
		}
	}

	sort.Slice(s.Years, func(i, j int) bool { return s.Years[i].Year > s.Years[j].Year })
	for _, y := range s.Years {
		sort.Slice(y.Months, func(i, j int) bool { return y.Months[i].Month > y.Months[j].Month })
	}
}

// AllMonths is every month of the archive, newest first.
func (s *Site) AllMonths() []*Month {
	var months []*Month
	for _, y := range s.Years {
		months = append(months, y.Months...)
	}
	return months
}

var instantRE = regexp.MustCompile(`\A(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(Z|[-+]\d\d:\d\d)?\z`)

// instant is a datetime as seconds since the epoch, if it is a datetime.
// Without an offset, it's taken as UTC, which is close enough for sorting.
// An impossible date (a typo, say, in metadata edited by hand) is not one.
func instant(datetime string) (int64, bool) {
	m := instantRE.FindStringSubmatch(datetime)
	if m == nil {
		return 0, false
	}
	var n [6]int
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], 0, time.UTC)
	if t.Year() != n[0] || int(t.Month()) != n[1] || t.Day() != n[2] ||
		t.Hour() != n[3] || t.Minute() != n[4] || t.Second() != n[5] {
		return 0, false
	}

	epoch := t.Unix()
	if zone := m[7]; len(zone) == 6 {
		hours, _ := strconv.Atoi(zone[1:3])
		minutes, _ := strconv.Atoi(zone[4:6])
		offset := int64(hours*3600 + minutes*60)
		if zone[0] == '-' {
			offset = -offset
		}
		epoch -= offset
	}
	return epoch, true
}

// HomeItem is one thing on the home page: an album, or a photo.
type HomeItem struct {
	Album *Album
	Photo *Photo
}

// HomeItems returns the n newest things for the home page, newest first:
// each a listed album, which stands for its photos, or a listed photo in no
// listed album.
//
// A photo is dated by when it was added, and an album by when its newest
// photo was added (or, if none of its photos has a date, when it was
// created).  So adding photos to an old album brings it back to the top,
// and the home page changes whenever anything new arrives.  (The feed dates
// albums by creation instead, so as not to announce an album again.)
// Photos added at the same moment come newest taken first, and anything
// with no date comes last.
func (s *Site) HomeItems(n int) []HomeItem {
	type dated struct {
		item  HomeItem
		when  int64
		known bool
		taken string
		name  string
	}
	var items []dated

	inAlbum := s.inListedAlbum()
	fold := cases.Fold()
	for _, a := range s.ListedAlbums {
		when, ok := instant(s.AlbumAdded(a))
		if !ok {
			when, ok = instant(a.Created)
		}
		items = append(items, dated{item: HomeItem{Album: a}, when: when, known: ok, name: fold.String(a.Title)})
	}
	for _, p := range s.Listed {
		if inAlbum[p.ID] {
			continue
		}
		when, ok := instant(p.AddedAt())
		items = append(items, dated{item: HomeItem{Photo: p}, when: when, known: ok, taken: p.Taken, name: p.ID})
	}

	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.known != b.known {
			return a.known
		}
		if a.when != b.when {
			return a.when > b.when
		}
		if a.taken != b.taken {
			return a.taken > b.taken
		}
		return a.name < b.name
	})

	if len(items) > n {
		items = items[:n]
	}
	home := make([]HomeItem, len(items))
	for i, d := range items {
		home[i] = d.item
	}
	return home
}

func (s *Site) inListedAlbum() map[string]bool {
	in := map[string]bool{}
	for _, a := range s.ListedAlbums {
		for _, p := range a.Photos {
			in[p.ID] = true
		}
	}
	return in
}

// AlbumAdded returns when an album's newest photo was added, as a
// datetime, or an empty string if none of them has a date.
func (s *Site) AlbumAdded(a *Album) string {
	newest, found := "", false
	var newestAt int64
	for _, p := range a.Photos {
		if at, ok := instant(p.AddedAt()); ok && (!found || at > newestAt) {
			newest, newestAt, found = p.AddedAt(), at, true
		}
	}
	return newest
}

// PublicLocation returns the photo's location as it may be published, or
// nil.  A photo taken inside any private zone has no public location,
// though its metadata keeps the true one.  Neither does a photo whose
// location is itself marked private.
//
// Every published location is also rounded, to LocationPrecision decimal
// places (3 by default, which is about 100 meters).  That's plenty for a
// map of where photos were taken, and it means no published photo pins down
// an exact spot.  It also blurs the edge of each private zone, where photos
// just outside would otherwise trace a ring around the hidden center.
//
// This is the only place published coordinates come from, so anything that
// publishes a location must get it here.
func (s *Site) PublicLocation(p *Photo) *Location {
	loc := p.Location
	if loc == nil || loc.Private {
		return nil
	}

	// The zone check uses the true location, so rounding can't move a
	// photo out of a zone.
	for _, zone := range s.Config.PrivateZones {
		if distanceM(loc.Lat, loc.Lon, zone.Lat, zone.Lon) <= zone.Radius {
			return nil
		}
	}

	round := func(v float64) float64 {
		rounded, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', s.Config.LocationPrecision, 64), 64)
		return rounded
	}
	return &Location{Lat: round(loc.Lat), Lon: round(loc.Lon)}
}

const earthRadiusM = 6_371_000

// distanceM returns the great-circle distance between two points, in
// meters, by the haversine formula.  At the scale of a private zone (a few
// hundred meters around a house) treating the Earth as a sphere is more
// than accurate enough.
func distanceM(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dlat := (lat2 - lat1) * rad
	dlon := (lon2 - lon1) * rad

	h := math.Pow(math.Sin(dlat/2), 2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Pow(math.Sin(dlon/2), 2)

	return 2 * earthRadiusM * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// isAlnum is whether a character is a letter or a digit, as Perl's
// \p{Alnum} has it: alphabetic, or a decimal number.
func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Nl, r) ||
		unicode.Is(unicode.Other_Alphabetic, r)
}

// TagSlug is a tag as it's compared and named in URLs: lowercase, with each
// run of anything but letters and digits made a hyphen, and none at either
// end.  So "high st" and "high-st" are one tag.
func TagSlug(tag string) string {
	var slug strings.Builder
	gap := false
	for _, r := range strings.ToLower(tag) {
		if !isAlnum(r) {
			gap = true
			continue
		}
		if gap && slug.Len() > 0 {
			slug.WriteByte('-')
		}
		gap = false
		slug.WriteRune(r)
	}
	if slug.Len() == 0 {
		return "-"
	}
	return slug.String()
}

// Sample returns up to n of the given photos, spread evenly through the
// list and kept in order, for showing a preview of a year or month.
func Sample(n int, photos []*Photo) []*Photo {
	if len(photos) <= n {
		return photos
	}
	few := make([]*Photo, n)
	for i := range few {
		few[i] = photos[i*len(photos)/n]
	}
	return few
}

var months = []string{
	"January", "February", "March", "April", "May", "June", "July",
	"August", "September", "October", "November", "December",
}

// MonthName is the name of a month given as its number, like "07".
func MonthName(month string) string {
	n, err := strconv.Atoi(month)
	if err != nil || n < 1 || n > 12 {
		return ""
	}
	return months[n-1]
}

var displayDateRE = regexp.MustCompile(`\A(\d{4})-(\d\d)-(\d\d)T(\d\d:\d\d)`)

// DisplayDate is when a photo was taken, like "6 January 2008, 19:36", or
// empty if it has no date.
func DisplayDate(p *Photo) string {
	m := displayDateRE.FindStringSubmatch(p.Taken)
	if m == nil {
		return p.Taken
	}
	day, _ := strconv.Atoi(m[3])
	year, _ := strconv.Atoi(m[1])
	return fmt.Sprintf("%d %s %d, %s", day, MonthName(m[2]), year, m[4])
}

// DisplayTitle is what a photo is called: its title, or, having none, when
// it was taken, or "Untitled".
func DisplayTitle(p *Photo) string {
	if p.Title != "" {
		return p.Title
	}
	if date := DisplayDate(p); date != "" {
		return date
	}
	return "Untitled"
}

var dayRE = regexp.MustCompile(`\A(\d{4})-(\d\d)-(\d\d)`)

// DisplayDay is a datetime's day, like "30 September 2026"; it's empty if
// it isn't given a datetime.
func DisplayDay(datetime string) string {
	m := dayRE.FindStringSubmatch(datetime)
	if m == nil {
		return ""
	}
	day, _ := strconv.Atoi(m[3])
	year, _ := strconv.Atoi(m[1])
	return fmt.Sprintf("%d %s %d", day, MonthName(m[2]), year)
}

// DisplayDuration is a length of time in seconds as "0:42", or "1:02:03".
func DisplayDuration(seconds float64) string {
	s := int(seconds + 0.5)
	h, m := s/3600, s%3600/60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s%60)
	}
	return fmt.Sprintf("%d:%02d", m, s%60)
}

// Count is a number of things, like "1 photo" or "48 photos".
func Count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// GridCaption returns the caption shown over a grid tile when it's hovered:
// its title, and a line of details, like "48 photos, added 30 September
// 2026" or "1:42, added 30 September 2026".
func (s *Site) GridCaption(item HomeItem) (name, details string) {
	var parts []string
	var added string

	if a := item.Album; a != nil {
		name = a.Title
		parts = append(parts, Count(len(a.Photos), "photo"))
		added = s.AlbumAdded(a)
	} else {
		p := item.Photo
		name = DisplayTitle(p)
		if p.IsVideo() && p.Duration != 0 {
			parts = append(parts, DisplayDuration(p.Duration))
		}
		added = p.AddedAt()
	}

	if day := DisplayDay(added); day != "" {
		parts = append(parts, "added "+day)
	}
	return name, strings.Join(parts, ", ")
}

// Excerpt is the start of some text, with its runs of whitespace made
// single spaces, cut at a word if it's longer than max characters.
func Excerpt(text string, max int) string {
	text = strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)[:max-1]

	// Drop the word the cut fell in, and the space before it.
	cut := len(runes)
	for cut > 0 && !unicode.IsSpace(runes[cut-1]) {
		cut--
	}
	if cut > 0 {
		for cut > 0 && unicode.IsSpace(runes[cut-1]) {
			cut--
		}
		runes = runes[:cut]
	}
	return string(runes) + "\u2026"
}

// URLs.  Every one is a path from the site's root.

func YearURL(year string) string         { return "/" + year + "/" }
func MonthURL(year, month string) string { return "/" + year + "/" + month + "/" }
func PhotoURL(p *Photo) string           { return "/p/" + p.ID + "/" }
func AlbumURL(a *Album) string           { return "/albums/" + a.Slug + "/" }
func TagURL(slug string) string          { return "/tags/" + slug + "/" }

// AlbumPhotoURL is a photo's page within an album, whose neighbors are the
// album's.
func AlbumPhotoURL(a *Album, p *Photo) string { return "/albums/" + a.Slug + "/" + p.ID + "/" }

// RenditionURL is where a rendition is published.  Renditions live apart
// from pages, so that the CDN's cache of pages can be purged by prefix
// without purging the renditions, which rarely change.
func RenditionURL(p *Photo, name string) string { return "/img/" + p.ID + "/" + name }

// Absolute is a path as a whole URL.
func (s *Site) Absolute(path string) string { return s.Config.BaseURL + path }

// Srcset is the srcset of a photo's image: its renditions and their widths.
func Srcset(p *Photo) string {
	var parts []string
	for _, name := range []string{"500.webp", "1024.webp", "2048.webp"} {
		parts = append(parts, fmt.Sprintf("%s %dw", RenditionURL(p, name), p.Renditions[name].Width))
	}
	return strings.Join(parts, ", ")
}

package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// libraryFormat is the format of TOML library this reads: the last one
// there will be.  An older library wants "jiggle upgrade" first.
const libraryFormat = 2

// datetime is a TOML datetime, kept as the text it had in its file: a
// wall-clock time, with a UTC offset if it was given one.
type datetime string

// UnmarshalTOML puts back the text the parser took apart.  The parser gives
// a time.Time, and says which form it had by the time's location: one of
// its own for a datetime with no offset, time.UTC itself for one written
// with a Z, and some other location, even at offset zero, for one written
// with a numeric offset.  Perl jiggle keeps the text and sorts by it, so
// Z and +00:00 must each come back as they were.  -- claude, 2026-11-22
func (d *datetime) UnmarshalTOML(v any) error {
	t, ok := v.(time.Time)
	if !ok {
		return fmt.Errorf("wanted a datetime, and got %T", v)
	}
	const wallClock = "2006-01-02T15:04:05.999999999"
	switch {
	case t.Location().String() == "datetime-local":
		*d = datetime(t.Format(wallClock))
	case t.Location() == time.UTC:
		*d = datetime(t.Format(wallClock) + "Z")
	case strings.HasPrefix(t.Location().String(), "date"), strings.HasPrefix(t.Location().String(), "time"):
		return fmt.Errorf("wanted a datetime, and got only a date or a time")
	default:
		*d = datetime(t.Format(wallClock + "-07:00"))
	}
	return nil
}

func (d *datetime) ptr() *string {
	if d == nil {
		return nil
	}
	s := string(*d)
	return &s
}

type configFile struct {
	Format            int     `toml:"format"`
	Title             *string `toml:"title"`
	BaseURL           *string `toml:"base_url"`
	Author            *string `toml:"author"`
	LocationPrecision *int64  `toml:"location_precision"`
	PrivateZones      []zone  `toml:"private_zone"`
	Publish           any     `toml:"publish"`
}

type zone struct {
	Lat    float64 `toml:"lat"`
	Lon    float64 `toml:"lon"`
	Radius float64 `toml:"radius"`
}

type photoFile struct {
	ID             string    `toml:"id"`
	Type           string    `toml:"type"`
	Title          string    `toml:"title"`
	Description    string    `toml:"description"`
	Taken          *datetime `toml:"taken"`
	Tags           []string  `toml:"tags"`
	Visibility     string    `toml:"visibility"`
	Rotate         int64     `toml:"rotate"`
	Added          *datetime `toml:"added"`
	FlickrID       *string   `toml:"flickr_id"`
	FlickrUploaded *datetime `toml:"flickr_uploaded"`

	Original *struct {
		File        string    `toml:"file"`
		Ext         string    `toml:"ext"`
		SHA256      string    `toml:"sha256"`
		Bytes       int64     `toml:"bytes"`
		Width       int64     `toml:"width"`
		Height      int64     `toml:"height"`
		Duration    *float64  `toml:"duration"`
		SourceMtime *datetime `toml:"source_mtime"`
	} `toml:"original"`

	Location *struct {
		Lat     float64 `toml:"lat"`
		Lon     float64 `toml:"lon"`
		Private bool    `toml:"private"`
	} `toml:"location"`
}

type albumFile struct {
	slug string

	Title       *string   `toml:"title"`
	Description string    `toml:"description"`
	Created     *datetime `toml:"created"`
	Cover       *string   `toml:"cover"`
	FlickrID    *string   `toml:"flickr_id"`
	Photos      []string  `toml:"photos"`
}

// library is a TOML library, read whole.
type library struct {
	root   string
	config configFile
	photos []*photoFile
	albums []*albumFile

	// warnings are things Perl jiggle puts up with, and so does this.
	// problems are things that would lose or mangle data, and stop a
	// migration.
	warnings []string
	problems []string
}

func (l *library) warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func (l *library) problemf(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

func shard(id string) string {
	if len(id) < 2 {
		return id
	}
	return id[:2]
}

// originalPath is where a photo's original is, relative to the library.
func originalPath(id, ext string) string {
	return filepath.Join("originals", shard(id), id+"."+ext)
}

// decodeFile reads a TOML file into v.  A key that v has no place for is an
// error: Perl jiggle would ignore it, but here it would be data left behind.
// The parser counts everything inside a table decoded as an any as not
// decoded, so such a table is named in open, and its keys are let by.
func decodeFile(path string, v any, open ...string) error {
	md, err := toml.DecodeFile(path, v)
	if err != nil {
		return err
	}
	var keys []string
	for _, k := range md.Undecoded() {
		if !slices.Contains(open, k[0]) {
			keys = append(keys, k.String())
		}
	}
	if len(keys) > 0 {
		return fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	return nil
}

var (
	sha256RE = regexp.MustCompile(`\A[0-9a-f]{64}\z`)
	extRE    = regexp.MustCompile(`\A[a-z0-9]+\z`)
)

// readLibrary reads the configuration, photos and albums of the library at
// root.  It returns an error only if it can't make a start; what's wrong
// with any one file is among the library's problems.
func readLibrary(root string) (*library, error) {
	l := &library{root: root}

	if err := decodeFile(filepath.Join(root, "jiggle.toml"), &l.config, "publish"); err != nil {
		return nil, fmt.Errorf("reading jiggle.toml: %w", err)
	}
	if l.config.Format != libraryFormat {
		return nil, fmt.Errorf("the library at %s is format %d, and migrate reads only format %d",
			root, l.config.Format, libraryFormat)
	}
	if l.config.Publish != nil {
		l.warnf("jiggle.toml: [publish] is not migrated")
	}

	if err := l.readPhotos(); err != nil {
		return nil, err
	}
	if err := l.readAlbums(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *library) readPhotos() error {
	meta := filepath.Join(l.root, "meta")
	shards, err := os.ReadDir(meta)
	if err != nil {
		return err
	}

	capitals := 0
	for _, dir := range shards {
		if !dir.IsDir() || dir.Name() == "albums" || dir.Name() == "flickr" || strings.HasPrefix(dir.Name(), ".") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(meta, dir.Name()))
		if err != nil {
			return err
		}
		for _, file := range files {
			stem, isTOML := strings.CutSuffix(file.Name(), ".toml")
			if !isTOML {
				continue
			}
			rel := filepath.Join("meta", dir.Name(), file.Name())

			var p photoFile
			if err := decodeFile(filepath.Join(l.root, rel), &p); err != nil {
				l.problemf("%s: %v", rel, err)
				continue
			}
			if p.ID != stem || shard(p.ID) != dir.Name() {
				l.problemf("%s: its id is %q", rel, p.ID)
				continue
			}
			if l.checkPhoto(rel, &p) {
				capitals++
			}
			l.photos = append(l.photos, &p)
		}
	}
	if capitals > 0 {
		l.warnf("%d photo(s) had tags with capitals, now lowercase", capitals)
	}
	return nil
}

// checkPhoto gives a photo the defaults Perl jiggle gives it, makes its tags
// lowercase and unique as Perl jiggle does, and notes what's wrong with it.
// It reports whether any tag had capitals.
func (l *library) checkPhoto(rel string, p *photoFile) (hadCapitals bool) {
	if p.Type == "" {
		p.Type = "photo"
	}
	if p.Type != "photo" && p.Type != "video" {
		l.problemf("%s: unknown type %q", rel, p.Type)
	}
	if p.Visibility == "" {
		p.Visibility = "public"
	}
	switch p.Visibility {
	case "public", "unlisted", "private", "pending":
	default:
		l.problemf("%s: unknown visibility %q", rel, p.Visibility)
	}
	switch p.Rotate {
	case 0, 90, 180, 270:
	default:
		l.problemf("%s: rotate must be 0, 90, 180, or 270, not %d", rel, p.Rotate)
	}

	seen := map[string]bool{}
	tags := make([]string, 0, len(p.Tags))
	for _, tag := range p.Tags {
		lower := strings.ToLower(tag)
		if lower != tag {
			hadCapitals = true
		}
		if !seen[lower] {
			seen[lower] = true
			tags = append(tags, lower)
		}
	}
	p.Tags = tags

	o := p.Original
	if o == nil {
		l.problemf("%s: no [original]", rel)
		return
	}
	if !sha256RE.MatchString(o.SHA256) {
		l.problemf("%s: its sha256 is not a SHA-256", rel)
	}
	if !extRE.MatchString(o.Ext) {
		l.problemf("%s: unknown ext %q", rel, o.Ext)
		return
	}

	file := originalPath(p.ID, o.Ext)
	switch info, err := os.Stat(filepath.Join(l.root, file)); {
	case errors.Is(err, os.ErrNotExist):
		l.problemf("%s: its original, %s, is missing", rel, file)
	case err != nil:
		l.problemf("%s: %v", rel, err)
	case info.Size() != o.Bytes:
		l.problemf("%s: its original, %s, is %d bytes, not %d", rel, file, info.Size(), o.Bytes)
	}
	return
}

func (l *library) readAlbums() error {
	dir := filepath.Join(l.root, "meta", "albums")
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	known := make(map[string]bool, len(l.photos))
	for _, p := range l.photos {
		known[p.ID] = true
	}

	for _, file := range files {
		slug, isTOML := strings.CutSuffix(file.Name(), ".toml")
		if !isTOML {
			continue
		}
		rel := filepath.Join("meta", "albums", file.Name())

		a := albumFile{slug: slug}
		if err := decodeFile(filepath.Join(dir, file.Name()), &a); err != nil {
			l.problemf("%s: %v", rel, err)
			continue
		}
		if a.Title == nil {
			l.problemf("%s: no title", rel)
			continue
		}

		// A photo is in an album once; Perl jiggle keeps the first.
		seen := map[string]bool{}
		photos := make([]string, 0, len(a.Photos))
		for _, id := range a.Photos {
			if seen[id] {
				continue
			}
			seen[id] = true
			photos = append(photos, id)
			if !known[id] {
				l.problemf("%s: lists photo %s, which doesn't exist", rel, id)
			}
		}
		if len(photos) < len(a.Photos) {
			l.warnf("%s: listed %d photo(s) twice", rel, len(a.Photos)-len(photos))
		}
		a.Photos = photos

		if a.Cover != nil && !known[*a.Cover] {
			l.problemf("%s: its cover is photo %s, which doesn't exist", rel, *a.Cover)
		}
		l.albums = append(l.albums, &a)
	}

	sort.Slice(l.albums, func(i, j int) bool { return l.albums[i].slug < l.albums[j].slug })
	return nil
}

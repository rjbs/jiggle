package site

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/rjbs/jiggle/internal/derive"
)

// Library is everything a build reads from the database, read in one
// transaction, so that a build is of the library at one moment.
type Library struct {
	Config Config
	Photos []*Photo
	Albums []*AlbumRecord
}

// Config is the site's own settings.
type Config struct {
	// Title is the site's title, "Photos" if none was set.
	Title string

	// BaseURL is the site's URL with no slash at its end, or empty.  Paths
	// are appended to it, and all start with a slash.
	BaseURL string

	// Author is who the feed says the photos are by: the title, if no
	// author was set.
	Author string

	// LocationPrecision is how many decimal places a published location
	// has.
	LocationPrecision int

	PrivateZones []Zone
}

// Zone is a circle, its radius in meters, in which photos publish no
// location.
type Zone struct {
	Lat, Lon, Radius float64
}

// Photo is a photo or video.  A date it doesn't have is an empty string.
type Photo struct {
	ID          string
	Type        string
	Title       string
	Description string
	Tags        []string
	Visibility  string

	// Taken is a wall-clock time, with a UTC offset only if one is known.
	Taken string

	Added          string
	FlickrUploaded string

	// Width and Height are of the picture as it's shown: upright, and
	// after the photo's extra turn.
	Width, Height int64

	// Duration is a video's length in seconds, or zero.
	Duration float64

	Location *Location

	// HasOriginal is whether the photo has an original yet.  One that is
	// still being imported has none, and is not published.
	HasOriginal bool

	// Renditions is the renditions the photo is shown by now, by name.
	Renditions map[string]*Rendition
}

// Location is where a photo was taken.  A private one is never published.
type Location struct {
	Lat, Lon float64
	Private  bool
}

// Rendition is one rendition of a photo: its own size in pixels, its
// blobId, and where its file is, relative to the library.
type Rendition struct {
	Width, Height int64
	BlobID        string
	Path          string
}

// AlbumRecord is an album as the database has it: its photos by id, in
// order, published or not.
type AlbumRecord struct {
	Slug        string
	Title       string
	Description string
	Created     string
	Cover       string
	PhotoIDs    []string
}

// IsPublished is whether the photo's visibility gives it a page on the
// site: whether it's public or unlisted.
func (p *Photo) IsPublished() bool { return p.Visibility == "public" || p.Visibility == "unlisted" }

// IsListed is whether the site lists the photo, on the home page, in the
// feed, and so on: whether it's public.
func (p *Photo) IsListed() bool { return p.Visibility == "public" }

func (p *Photo) IsVideo() bool { return p.Type == "video" }

// AddedAt is when the photo joined the collection: Added, or, for photos
// imported before there was such a thing, FlickrUploaded, which means the
// same.  It may be empty.
func (p *Photo) AddedAt() string {
	if p.Added != "" {
		return p.Added
	}
	return p.FlickrUploaded
}

// Rendered is whether the photo has every rendition the site publishes.
func (p *Photo) Rendered() bool {
	for _, recipe := range derive.RecipesFor(p.Type) {
		if recipe.Publish && p.Renditions[recipe.Name] == nil {
			return false
		}
	}
	return true
}

// Queryer is what Load reads through: a transaction, so that everything it
// reads is of one moment.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Load reads the library from the database.
func Load(ctx context.Context, q Queryer) (*Library, error) {
	l := &loader{ctx: ctx, q: q}
	lib := &Library{Config: l.config(), Photos: l.photos(), Albums: l.albums()}
	return lib, l.err
}

// loader keeps the first error it meets, and after that does nothing.
type loader struct {
	ctx context.Context
	q   Queryer
	err error
}

func (l *loader) rows(query string, each func(scan func(dest ...any))) {
	if l.err != nil {
		return
	}
	rows, err := l.q.QueryContext(l.ctx, query)
	if err != nil {
		l.err = err
		return
	}
	defer rows.Close()
	for rows.Next() {
		each(func(dest ...any) {
			if err := rows.Scan(dest...); err != nil && l.err == nil {
				l.err = err
			}
		})
	}
	if err := rows.Err(); err != nil && l.err == nil {
		l.err = err
	}
}

func (l *loader) config() Config {
	c := Config{Title: "Photos", LocationPrecision: 3}
	author := false
	l.rows(`SELECT name, value FROM setting`, func(scan func(...any)) {
		var name, value string
		scan(&name, &value)
		switch name {
		case "title":
			c.Title = value
		case "base_url":
			for len(value) > 0 && value[len(value)-1] == '/' {
				value = value[:len(value)-1]
			}
			c.BaseURL = value
		case "author":
			c.Author, author = value, true
		case "location_precision":
			places, err := strconv.Atoi(value)
			if err != nil && l.err == nil {
				l.err = fmt.Errorf("the location_precision setting: %w", err)
			}
			c.LocationPrecision = places
		}
	})
	if !author {
		c.Author = c.Title
	}
	l.rows(`SELECT lat, lon, radius FROM private_zone ORDER BY rowid`, func(scan func(...any)) {
		var z Zone
		scan(&z.Lat, &z.Lon, &z.Radius)
		c.PrivateZones = append(c.PrivateZones, z)
	})
	return c
}

func (l *loader) photos() []*Photo {
	tags := map[string][]string{}
	l.rows(`SELECT photo_id, tag FROM photo_tag ORDER BY photo_id, tag`, func(scan func(...any)) {
		var id, tag string
		scan(&id, &tag)
		tags[id] = append(tags[id], tag)
	})

	renditions := map[string]map[string]*Rendition{}
	l.rows(`
		SELECT photo_id, name, width, height, blob.id, blob.path
		FROM photo_rendition JOIN blob ON blob.id = blob_id`,
		func(scan func(...any)) {
			var id, name string
			var r Rendition
			scan(&id, &name, &r.Width, &r.Height, &r.BlobID, &r.Path)
			if renditions[id] == nil {
				renditions[id] = map[string]*Rendition{}
			}
			renditions[id][name] = &r
		})

	var photos []*Photo
	l.rows(`
		SELECT id, type, title, description, coalesce(taken, ''), visibility, rotate,
		       coalesce(added, ''), coalesce(flickr_uploaded, ''),
		       lat, lon, location_private,
		       coalesce(width, 0), coalesce(height, 0), coalesce(duration, 0),
		       blob_id IS NOT NULL
		FROM photo ORDER BY id`,
		func(scan func(...any)) {
			var p Photo
			var rotate int64
			var lat, lon sql.NullFloat64
			var private bool
			scan(&p.ID, &p.Type, &p.Title, &p.Description, &p.Taken, &p.Visibility, &rotate,
				&p.Added, &p.FlickrUploaded, &lat, &lon, &private,
				&p.Width, &p.Height, &p.Duration, &p.HasOriginal)

			if rotate%180 != 0 {
				p.Width, p.Height = p.Height, p.Width
			}
			if lat.Valid {
				p.Location = &Location{Lat: lat.Float64, Lon: lon.Float64, Private: private}
			}
			p.Tags = tags[p.ID]
			p.Renditions = renditions[p.ID]
			photos = append(photos, &p)
		})
	return photos
}

func (l *loader) albums() []*AlbumRecord {
	members := map[string][]string{}
	l.rows(`SELECT album_id, photo_id FROM album_photo ORDER BY album_id, position`, func(scan func(...any)) {
		var album, photo string
		scan(&album, &photo)
		members[album] = append(members[album], photo)
	})

	var albums []*AlbumRecord
	l.rows(`
		SELECT id, slug, title, description, coalesce(created, ''), coalesce(cover_photo_id, '')
		FROM album ORDER BY slug`,
		func(scan func(...any)) {
			var id string
			var a AlbumRecord
			scan(&id, &a.Slug, &a.Title, &a.Description, &a.Created, &a.Cover)
			a.PhotoIDs = members[id]
			albums = append(albums, &a)
		})
	return albums
}

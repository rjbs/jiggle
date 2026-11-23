package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"

	"github.com/rjbs/gizmo/sqlitekit"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// CheckReport says what a check compared, and what it found.
type CheckReport struct {
	Photos int
	Albums int

	// Differences are the places where the database and Perl jiggle
	// disagree, each naming an object and a property.
	Differences []string
}

// Check compares a migrated database with what Perl jiggle loads from the
// library it was made from, as printed by "jiggle dump".  The two read the
// same files with different TOML parsers and different code, so agreement
// here is the evidence that the migration read the library rightly.
//
// The database's side is put into the dump's own shape, as it would come
// from decoding JSON, and the two are then compared value by value.
func Check(ctx context.Context, dbPath string, dump io.Reader) (*CheckReport, error) {
	var perl struct {
		Config map[string]any   `json:"config"`
		Photos []map[string]any `json:"photos"`
		Albums []map[string]any `json:"albums"`
	}
	if err := json.NewDecoder(dump).Decode(&perl); err != nil {
		return nil, fmt.Errorf("reading the dump: %w", err)
	}
	if perl.Config == nil || perl.Photos == nil || perl.Albums == nil {
		return nil, fmt.Errorf("reading the dump: it is not what jiggle dump prints")
	}

	db, err := sqlitekit.OpenReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := store.Kit.Read(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	r := &reader{ctx: ctx, tx: tx}
	config := r.config()
	photos := r.photos()
	albums := r.albums()
	if r.err != nil {
		return nil, r.err
	}

	report := &CheckReport{Photos: len(photos), Albums: len(albums)}
	c := &comparison{}
	c.compare("config", perl.Config, config)
	c.compareAll("photo", "id", perl.Photos, photos)
	c.compareAll("album", "slug", perl.Albums, albums)
	report.Differences = c.differences
	return report, nil
}

// reader reads the database into the shape of the dump.  It keeps the first
// error it meets, and after that does nothing.
type reader struct {
	ctx context.Context
	tx  *sqlitekit.Tx
	err error
}

// rows runs a query, and calls each with a function that scans the current
// row.
func (r *reader) rows(query string, each func(scan func(dest ...any))) {
	if r.err != nil {
		return
	}
	rows, err := r.tx.QueryContext(r.ctx, query)
	if err != nil {
		r.err = err
		return
	}
	defer rows.Close()
	for rows.Next() {
		each(func(dest ...any) {
			if err := rows.Scan(dest...); err != nil && r.err == nil {
				r.err = err
			}
		})
	}
	if err := rows.Err(); err != nil && r.err == nil {
		r.err = err
	}
}

func str(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func num(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func (r *reader) config() map[string]any {
	config := map[string]any{
		"title": nil, "base_url": nil, "author": nil, "location_precision": nil,
		"private_zone": []any{},
	}
	r.rows(`SELECT name, value FROM setting`, func(scan func(...any)) {
		var name, value string
		scan(&name, &value)
		if name == "location_precision" {
			places, err := strconv.ParseFloat(value, 64)
			if err != nil && r.err == nil {
				r.err = fmt.Errorf("location_precision: %w", err)
			}
			config[name] = places
			return
		}
		config[name] = value
	})
	r.rows(`SELECT lat, lon, radius FROM private_zone ORDER BY rowid`, func(scan func(...any)) {
		var lat, lon, radius float64
		scan(&lat, &lon, &radius)
		config["private_zone"] = append(config["private_zone"].([]any),
			map[string]any{"lat": lat, "lon": lon, "radius": radius})
	})
	return config
}

func (r *reader) photos() []map[string]any {
	tags := map[string][]any{}
	r.rows(`SELECT photo_id, tag FROM photo_tag ORDER BY photo_id, position`, func(scan func(...any)) {
		var id, tag string
		scan(&id, &tag)
		tags[id] = append(tags[id], tag)
	})

	sizes := map[string]map[string]any{}
	r.rows(`
		SELECT photo_id, name, width, height FROM photo_rendition`,
		func(scan func(...any)) {
			var id, name string
			var width, height float64
			scan(&id, &name, &width, &height)
			if sizes[id] == nil {
				sizes[id] = map[string]any{}
			}
			sizes[id][name] = []any{width, height}
		})

	var photos []map[string]any
	r.rows(`
		SELECT photo.id, type, title, description, taken, visibility, rotate,
		       added, flickr_id, flickr_uploaded, lat, lon, location_private,
		       original_name, original_mtime, width, height, duration,
		       substr(blob.id, 2), blob.bytes, blob.ext
		FROM photo LEFT JOIN blob ON blob.id = blob_id
		ORDER BY photo.id`,
		func(scan func(...any)) {
			var (
				id, mediaType, title, description, visibility       string
				taken, added, flickrID, flickrUploaded, name, mtime sql.NullString
				digest, ext                                         sql.NullString
				rotate                                              float64
				lat, lon, width, height, duration, bytes            sql.NullFloat64
				private                                             bool
			)
			scan(&id, &mediaType, &title, &description, &taken, &visibility, &rotate,
				&added, &flickrID, &flickrUploaded, &lat, &lon, &private,
				&name, &mtime, &width, &height, &duration,
				&digest, &bytes, &ext)

			var location any
			if lat.Valid {
				location = map[string]any{"lat": lat.Float64, "lon": lon.Float64, "private": private}
			}

			rendered := true
			for _, recipe := range derive.RecipesFor(mediaType) {
				if _, ok := sizes[id][recipe.Name]; recipe.Publish && !ok {
					rendered = false
				}
			}
			if tags[id] == nil {
				tags[id] = []any{}
			}
			if sizes[id] == nil {
				sizes[id] = map[string]any{}
			}

			photos = append(photos, map[string]any{
				"id": id, "type": mediaType, "title": title, "description": description,
				"taken": str(taken), "visibility": visibility, "rotate": rotate,
				"added": str(added), "flickr_id": str(flickrID), "flickr_uploaded": str(flickrUploaded),
				"tags":     tags[id],
				"location": location,
				"original": map[string]any{
					"file": str(name), "ext": str(ext), "sha256": str(digest), "source_mtime": str(mtime),
					"bytes": num(bytes), "width": num(width), "height": num(height), "duration": num(duration),
				},
				"rendered":        rendered,
				"rendition_sizes": sizes[id],
			})
		})
	return photos
}

func (r *reader) albums() []map[string]any {
	members := map[string][]any{}
	r.rows(`SELECT album_id, photo_id FROM album_photo ORDER BY album_id, position`, func(scan func(...any)) {
		var album, photo string
		scan(&album, &photo)
		members[album] = append(members[album], photo)
	})

	var albums []map[string]any
	r.rows(`
		SELECT id, slug, title, description, created, cover_photo_id, flickr_id
		FROM album ORDER BY slug`,
		func(scan func(...any)) {
			var id, slug, title, description string
			var created, cover, flickrID sql.NullString
			scan(&id, &slug, &title, &description, &created, &cover, &flickrID)
			if members[id] == nil {
				members[id] = []any{}
			}
			albums = append(albums, map[string]any{
				"slug": slug, "title": title, "description": description,
				"created": str(created), "cover": str(cover), "flickr_id": str(flickrID),
				"photos": members[id],
			})
		})
	return albums
}

type comparison struct {
	differences []string
}

func (c *comparison) differ(format string, args ...any) {
	c.differences = append(c.differences, fmt.Sprintf(format, args...))
}

// compareAll compares two lists of objects of one kind, pairing them by the
// property that names them.
func (c *comparison) compareAll(kind, key string, perl, db []map[string]any) {
	index := func(list []map[string]any) map[string]map[string]any {
		byName := make(map[string]map[string]any, len(list))
		for _, obj := range list {
			byName[fmt.Sprint(obj[key])] = obj
		}
		return byName
	}
	inPerl, inDB := index(perl), index(db)

	names := make([]string, 0, len(inPerl))
	for name := range inPerl {
		names = append(names, name)
	}
	for name := range inDB {
		if _, ok := inPerl[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		what := kind + " " + name
		switch {
		case inDB[name] == nil:
			c.differ("%s: Perl jiggle has it, and the database doesn't", what)
		case inPerl[name] == nil:
			c.differ("%s: the database has it, and Perl jiggle doesn't", what)
		default:
			c.compare(what, inPerl[name], inDB[name])
		}
	}
}

// compare compares the properties of one object.
func (c *comparison) compare(what string, perl, db map[string]any) {
	keys := make([]string, 0, len(perl))
	for key := range perl {
		keys = append(keys, key)
	}
	for key := range db {
		if _, ok := perl[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	for _, key := range keys {
		p, d := perl[key], db[key]

		// The dump gives the size every rendition would have.  The
		// database has only the ones that have been made.
		if key == "rendition_sizes" {
			if all, ok := p.(map[string]any); ok {
				made := map[string]any{}
				for name := range d.(map[string]any) {
					if size, ok := all[name]; ok {
						made[name] = size
					}
				}
				p = made
			}
		}

		if !reflect.DeepEqual(p, d) {
			c.differ("%s: %s: Perl jiggle has %s, and the database has %s", what, key, show(p), show(d))
		}
	}
}

func show(v any) string {
	text, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(text)
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"

	"github.com/rjbs/jiggle/internal/store"
)

// A Photo's properties.  The first lot are a person's to set; the rest are
// set only by the server.  See doc/types.md.
var (
	photoEditable = []string{"title", "description", "taken", "visibility", "rotate", "tags", "location"}
	photoFixed    = []string{"id", "type", "added", "flickrId", "flickrUploaded", "blobId", "original", "renditions"}
	photoProps    = append(append([]string{}, photoFixed...), photoEditable...)
)

func knownProperty(names []string) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
}

// loadPhotos returns the photos with the given ids, by id, or every photo
// when ids is nil, and the ids found, in order of id.
func loadPhotos(ctx context.Context, tx *txn, ids []string) (map[string]object, []string, error) {
	// where is a condition on the column that holds a photo's id.
	where := func(column string) (string, []any) {
		if ids == nil {
			return "", nil
		}
		return " WHERE " + among(column), []any{idList(ids)}
	}

	photos, found := map[string]object{}, []string{}

	cond, args := where("p.id")
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, p.type, p.title, p.description, p.taken, p.visibility, p.rotate, p.added,
		       p.flickr_id, p.flickr_uploaded, p.lat, p.lon, p.location_private,
		       p.blob_id, p.original_name, p.original_mtime, p.width, p.height, p.duration, b.bytes
		FROM photo p LEFT JOIN blob b ON b.id = p.blob_id`+cond+` ORDER BY p.id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, typ, title, description, visibility              string
			rotate                                               int
			private                                              bool
			taken, added, flickrID, flickrUploaded, blobID, name sql.NullString
			mtime                                                sql.NullString
			lat, lon, duration                                   sql.NullFloat64
			width, height, bytes                                 sql.NullInt64
		)
		err := rows.Scan(&id, &typ, &title, &description, &taken, &visibility, &rotate, &added,
			&flickrID, &flickrUploaded, &lat, &lon, &private,
			&blobID, &name, &mtime, &width, &height, &duration, &bytes)
		if err != nil {
			return nil, nil, err
		}
		p := object{
			"id": id, "type": typ, "title": title, "description": description,
			"taken": nullText(taken), "visibility": visibility, "rotate": rotate,
			"tags": map[string]bool{}, "location": nil,
			"added": nullText(added), "flickrId": nullText(flickrID), "flickrUploaded": nullText(flickrUploaded),
			"blobId": nullText(blobID), "original": nil, "renditions": map[string]object{},
		}
		if lat.Valid {
			p["location"] = object{"lat": lat.Float64, "lon": lon.Float64, "isPrivate": private}
		}
		if blobID.Valid {
			p["original"] = object{
				"name": nullText(name), "modified": nullText(mtime), "size": nullInt(bytes),
				"width": nullInt(width), "height": nullInt(height), "duration": nullFloat(duration),
			}
		}
		photos[id] = p
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	cond, args = where("photo_id")
	tags, err := tx.QueryContext(ctx, `SELECT photo_id, tag FROM photo_tag`+cond, args...)
	if err != nil {
		return nil, nil, err
	}
	defer tags.Close()
	for tags.Next() {
		var id, tag string
		if err := tags.Scan(&id, &tag); err != nil {
			return nil, nil, err
		}
		photos[id]["tags"].(map[string]bool)[tag] = true
	}
	if err := tags.Err(); err != nil {
		return nil, nil, err
	}

	cond, args = where("r.photo_id")
	renditions, err := tx.QueryContext(ctx, `
		SELECT r.photo_id, r.name, r.blob_id, r.width, r.height, b.bytes
		FROM photo_rendition r JOIN blob b ON b.id = r.blob_id`+cond, args...)
	if err != nil {
		return nil, nil, err
	}
	defer renditions.Close()
	for renditions.Next() {
		var id, name, blobID string
		var width, height, bytes int64
		if err := renditions.Scan(&id, &name, &blobID, &width, &height, &bytes); err != nil {
			return nil, nil, err
		}
		photos[id]["renditions"].(map[string]object)[name] = object{
			"blobId": blobID, "width": width, "height": height, "size": bytes,
		}
	}
	return photos, found, renditions.Err()
}

func (s *Server) photoGet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseGet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Get(c, s.begin, limits, std.Getter[*txn]{
		Type:          store.TypePhoto,
		KnownProperty: knownProperty(photoProps),
		Count: func(ctx context.Context, tx *txn) (n int, err error) {
			return n, tx.QueryRowContext(ctx, `SELECT count(*) FROM photo`).Scan(&n)
		},
		Load: func(ctx context.Context, tx *txn, ids, _ []string) (map[string]map[string]any, []string, error) {
			return loadPhotos(ctx, tx, ids)
		},
	}, req)
}

func (s *Server) photoChanges(c *jmap.Call, raw json.RawMessage) (any, error) {
	return s.changes(c, store.TypePhoto, raw)
}

// A time on the clock, as taken is: a date and time, with a UTC offset if
// one is known.
var takenRE = regexp.MustCompile(`\A\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(Z|[+-]\d\d:\d\d)?\z`)

// validTaken reports whether s is such a time, and one that there is.
func validTaken(s string) bool {
	m := takenRE.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	layout := "2006-01-02T15:04:05"
	if m[1] != "" {
		layout += "Z07:00"
	}
	_, err := time.Parse(layout, s)
	return err == nil
}

// checkPhoto checks a photo as a client would have it be, against the photo
// as it is, and returns it as it will be: the client's, with its tags in
// lowercase, as tags are kept.
func checkPhoto(old, asked object) (object, error) {
	c := check(asked)
	c.only(photoProps)
	c.unchanged(old, photoFixed...)

	now := object{}
	for name, value := range old {
		now[name] = value
	}

	now["title"] = c.text("title")
	now["description"] = c.text("description")

	taken := c.optionalText("taken")
	now["taken"] = nil
	if taken != nil {
		now["taken"] = *taken
		if !validTaken(*taken) {
			c.fail("taken")
		}
	}

	switch visibility := c.text("visibility"); visibility {
	case "public", "unlisted", "private", "pending":
		now["visibility"] = visibility
	default:
		c.fail("visibility")
	}

	switch rotate, _ := number(asked["rotate"]); rotate {
	case 0, 90, 180, 270:
		now["rotate"] = int(rotate)
		if _, ok := asked["rotate"].(json.Number); !ok {
			c.fail("rotate")
		}
	default:
		c.fail("rotate")
	}

	// The tags are a set: an object whose every value is true.
	tags := map[string]bool{}
	given, ok := asked["tags"].(map[string]any)
	if !ok {
		c.fail("tags")
	}
	for tag, value := range given {
		if value != true || strings.TrimSpace(tag) == "" || tag != strings.TrimSpace(tag) {
			c.fail("tags")
			continue
		}
		tags[strings.ToLower(tag)] = true
	}
	now["tags"] = tags

	switch location := asked["location"].(type) {
	case nil:
		now["location"] = nil
	case map[string]any:
		lat, okLat := number(location["lat"])
		lon, okLon := number(location["lon"])
		private, okPrivate := location["isPrivate"].(bool)
		if _, given := location["isPrivate"]; !given {
			okPrivate = true
		}
		for name := range location {
			if name != "lat" && name != "lon" && name != "isPrivate" {
				c.fail("location")
			}
		}
		if !okLat || !okLon || !okPrivate || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			c.fail("location")
		}
		now["location"] = object{"lat": lat, "lon": lon, "isPrivate": private}
	default:
		c.fail("location")
	}

	return now, c.err()
}

// updatePhoto applies a patch to a photo.
func updatePhoto(ctx context.Context, tx *txn, id string, raw json.RawMessage) (map[string]any, error) {
	photos, _, err := loadPhotos(ctx, tx, []string{id})
	if err != nil {
		return nil, err
	}
	old, ok := photos[id]
	if !ok {
		return nil, jmap.NotFound()
	}
	asked, err := patched(old, raw)
	if err != nil {
		return nil, err
	}
	now, err := checkPhoto(old, asked)
	if err != nil {
		return nil, err
	}
	if same(old, now) {
		return told(asked, now, photoEditable), nil
	}

	var lat, lon any
	private := false
	if location, ok := now["location"].(object); ok {
		lat, lon, private = location["lat"], location["lon"], location["isPrivate"].(bool)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE photo
		SET title = ?, description = ?, taken = ?, visibility = ?, rotate = ?, lat = ?, lon = ?, location_private = ?
		WHERE id = ?`,
		now["title"], now["description"], now["taken"], now["visibility"], now["rotate"], lat, lon, private, id)
	if err != nil {
		return nil, err
	}

	if err := writeTags(ctx, tx, id, old["tags"].(map[string]bool), now["tags"].(map[string]bool)); err != nil {
		return nil, err
	}

	if _, err := tx.Touch(ctx, store.TypePhoto, id); err != nil {
		return nil, err
	}
	return told(asked, now, photoEditable), nil
}

// writeTags makes a photo's tags what they are now.
func writeTags(ctx context.Context, tx *txn, id string, old, now map[string]bool) error {
	for tag := range old {
		if now[tag] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM photo_tag WHERE photo_id = ? AND tag = ?`, id, tag); err != nil {
			return err
		}
	}
	for tag := range now {
		if old[tag] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO photo_tag (photo_id, tag) VALUES (?, ?)`, id, tag); err != nil {
			return err
		}
	}
	return nil
}

// destroyPhoto removes a photo, and takes it out of every album it is in.
// An album whose cover it was gets its first remaining photo as its cover,
// or none.  The photo's original is not removed, and is still a blob.
func destroyPhoto(ctx context.Context, tx *txn, id string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT album_id FROM album_photo WHERE photo_id = ?1
		UNION SELECT id FROM album WHERE cover_photo_id = ?1`, id)
	if err != nil {
		return err
	}
	var albums []string
	for rows.Next() {
		var album string
		if err := rows.Scan(&album); err != nil {
			rows.Close()
			return err
		}
		albums = append(albums, album)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM album_photo WHERE photo_id = ?`, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE album
		SET cover_photo_id = (SELECT photo_id FROM album_photo WHERE album_id = album.id ORDER BY position LIMIT 1)
		WHERE cover_photo_id = ?`, id)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if _, err := tx.Touch(ctx, store.TypeAlbum, album); err != nil {
			return err
		}
	}

	found, err := tx.Destroy(ctx, store.TypePhoto, id)
	if err != nil {
		return err
	}
	if !found {
		return jmap.NotFound()
	}
	return nil
}

// Photo/set updates and destroys photos.  It doesn't create them: a photo
// is made from a file, by an import.
func (s *Server) photoSet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseSet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Set(c, s.begin, limits, std.Setter[*txn]{
		Type:    store.TypePhoto,
		Update:  updatePhoto,
		Destroy: destroyPhoto,
	}, req)
}

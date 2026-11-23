package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"

	"github.com/rjbs/jiggle/internal/store"
)

// An Album's properties.  The first lot are a person's to set; the rest
// are set only by the server.  See doc/types.md.
var (
	albumEditable = []string{"slug", "title", "description", "coverPhotoId", "photoIds"}
	albumFixed    = []string{"id", "created", "flickrId"}
	albumProps    = append(append([]string{}, albumFixed...), albumEditable...)
)

// loadAlbums returns the albums with the given ids, by id, or every album
// when ids is nil, and the ids found, in order of id.
func loadAlbums(ctx context.Context, tx *txn, ids []string) (map[string]object, []string, error) {
	where := func(column string) (string, []any) {
		if ids == nil {
			return "", nil
		}
		return " WHERE " + among(column), []any{idList(ids)}
	}

	albums, found := map[string]object{}, []string{}

	cond, args := where("id")
	rows, err := tx.QueryContext(ctx, `
		SELECT id, slug, title, description, created, cover_photo_id, flickr_id
		FROM album`+cond+` ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, slug, title, description string
		var created, cover, flickrID sql.NullString
		if err := rows.Scan(&id, &slug, &title, &description, &created, &cover, &flickrID); err != nil {
			return nil, nil, err
		}
		albums[id] = object{
			"id": id, "slug": slug, "title": title, "description": description,
			"coverPhotoId": nullText(cover), "photoIds": []string{},
			"created": nullText(created), "flickrId": nullText(flickrID),
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	cond, args = where("album_id")
	photos, err := tx.QueryContext(ctx, `SELECT album_id, photo_id FROM album_photo`+cond+` ORDER BY album_id, position`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer photos.Close()
	for photos.Next() {
		var id, photo string
		if err := photos.Scan(&id, &photo); err != nil {
			return nil, nil, err
		}
		albums[id]["photoIds"] = append(albums[id]["photoIds"].([]string), photo)
	}
	return albums, found, photos.Err()
}

func (s *Server) albumGet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseGet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Get(c, s.begin, limits, std.Getter[*txn]{
		Type:          store.TypeAlbum,
		KnownProperty: knownProperty(albumProps),
		Load: func(ctx context.Context, tx *txn, ids, _ []string) (map[string]map[string]any, []string, error) {
			return loadAlbums(ctx, tx, ids)
		},
	}, req)
}

func (s *Server) albumChanges(c *jmap.Call, raw json.RawMessage) (any, error) {
	return s.changes(c, store.TypeAlbum, raw)
}

// A slug is part of a URL and a term of a query: lowercase letters and
// digits, with single hyphens between runs of them.
var slugRE = regexp.MustCompile(`\A[\p{Ll}\p{Lo}\p{N}]+(?:-[\p{Ll}\p{Lo}\p{N}]+)*\z`)

// checkAlbum checks an album as a client would have it be, against the
// album as it is (nil, for one being created), and returns it as it will
// be.  Ids of photos may be creation references.
func checkAlbum(ctx context.Context, c *jmap.Call, tx *txn, id string, old, asked object) (object, error) {
	ck := check(asked)
	ck.only(albumProps)
	if old != nil {
		ck.unchanged(old, albumFixed...)
	} else {
		ck.unchanged(object{}, albumFixed...)
	}

	now := object{}
	for name, value := range old {
		now[name] = value
	}

	slug := ck.text("slug")
	if !slugRE.MatchString(slug) {
		ck.fail("slug")
	}
	now["slug"] = slug

	title := ck.text("title")
	if strings.TrimSpace(title) == "" {
		ck.fail("title")
	}
	now["title"] = title
	now["description"] = ck.text("description")

	// Every photo named must exist, and may be named once.
	exists := func(given any) (string, bool) {
		ref, ok := given.(string)
		if !ok {
			return "", false
		}
		photo, ok := c.ResolveID(ref)
		if !ok {
			return "", false
		}
		var found bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM photo WHERE id = ?)`, photo).Scan(&found)
		return photo, err == nil && found
	}

	photos, seen := []string{}, map[string]bool{}
	given, ok := asked["photoIds"].([]any)
	if !ok {
		ck.fail("photoIds")
	}
	for _, v := range given {
		photo, ok := exists(v)
		if !ok || seen[photo] {
			ck.fail("photoIds")
			continue
		}
		seen[photo] = true
		photos = append(photos, photo)
	}
	now["photoIds"] = photos

	now["coverPhotoId"] = nil
	if asked["coverPhotoId"] != nil {
		cover, ok := exists(asked["coverPhotoId"])
		if !ok {
			ck.fail("coverPhotoId")
		}
		now["coverPhotoId"] = cover
	}

	if err := ck.err(); err != nil {
		return nil, err
	}

	var other string
	err := tx.QueryRowContext(ctx, `SELECT id FROM album WHERE slug = ? AND id <> ?`, slug, id).Scan(&other)
	switch {
	case err == nil:
		return nil, jmap.AlreadyExists("there is already an album with the slug "+slug, other)
	case err != sql.ErrNoRows:
		return nil, err
	}
	return now, nil
}

// writeAlbumPhotos replaces the list of an album's photos.
func writeAlbumPhotos(ctx context.Context, tx *txn, id string, photos []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM album_photo WHERE album_id = ?`, id); err != nil {
		return err
	}
	for i, photo := range photos {
		_, err := tx.ExecContext(ctx, `INSERT INTO album_photo (album_id, position, photo_id) VALUES (?, ?, ?)`, id, i, photo)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createAlbum(c *jmap.Call, ctx context.Context, tx *txn, raw json.RawMessage) (string, map[string]any, error) {
	given, err := jmap.DecodeObject(raw)
	if err != nil {
		return "", nil, jmap.InvalidProperties(err.Error())
	}
	// What the client leaves out is as it would be by default.
	asked := object{"description": "", "coverPhotoId": nil, "photoIds": []any{}}
	for name, value := range given {
		asked[name] = value
	}
	now, err := checkAlbum(ctx, c, tx, "", nil, asked)
	if err != nil {
		return "", nil, err
	}

	id, err := tx.NewID(ctx, store.TypeAlbum)
	if err != nil {
		return "", nil, err
	}
	modseq, err := tx.Create(ctx, store.TypeAlbum, id)
	if err != nil {
		return "", nil, err
	}
	now["created"] = s.cfg.Now().Format("2006-01-02T15:04:05Z07:00")
	now["flickrId"] = nil
	_, err = tx.ExecContext(ctx, `
		INSERT INTO album (id, slug, title, description, created, cover_photo_id, created_modseq, modseq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, now["slug"], now["title"], now["description"], now["created"], now["coverPhotoId"], modseq, modseq)
	if err != nil {
		return "", nil, err
	}
	if err := writeAlbumPhotos(ctx, tx, id, now["photoIds"].([]string)); err != nil {
		return "", nil, err
	}
	return id, told(given, now, albumProps[1:]), nil
}

func updateAlbum(c *jmap.Call, ctx context.Context, tx *txn, id string, raw json.RawMessage) (map[string]any, error) {
	albums, _, err := loadAlbums(ctx, tx, []string{id})
	if err != nil {
		return nil, err
	}
	old, ok := albums[id]
	if !ok {
		return nil, jmap.NotFound()
	}
	asked, err := patched(old, raw)
	if err != nil {
		return nil, err
	}
	now, err := checkAlbum(ctx, c, tx, id, old, asked)
	if err != nil {
		return nil, err
	}
	if same(old, now) {
		return told(asked, now, albumEditable), nil
	}

	_, err = tx.ExecContext(ctx, `UPDATE album SET slug = ?, title = ?, description = ?, cover_photo_id = ? WHERE id = ?`,
		now["slug"], now["title"], now["description"], now["coverPhotoId"], id)
	if err != nil {
		return nil, err
	}
	if !same(old["photoIds"], now["photoIds"]) {
		if err := writeAlbumPhotos(ctx, tx, id, now["photoIds"].([]string)); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Touch(ctx, store.TypeAlbum, id); err != nil {
		return nil, err
	}
	return told(asked, now, albumEditable), nil
}

// Album/set creates, updates and destroys albums.  Destroying an album
// does nothing to its photos.
func (s *Server) albumSet(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseSet(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Set(c, s.begin, limits, std.Setter[*txn]{
		Type: store.TypeAlbum,
		Create: func(ctx context.Context, tx *txn, raw json.RawMessage) (string, map[string]any, error) {
			return s.createAlbum(c, ctx, tx, raw)
		},
		Update: func(ctx context.Context, tx *txn, id string, raw json.RawMessage) (map[string]any, error) {
			return updateAlbum(c, ctx, tx, id, raw)
		},
		Destroy: func(ctx context.Context, tx *txn, id string) error {
			found, err := tx.Destroy(ctx, store.TypeAlbum, id)
			if err == nil && !found {
				err = jmap.NotFound()
			}
			return err
		},
	}, req)
}

// The columns an album can be sorted by, by property.
var albumSorts = map[string]string{"slug": "slug", "title": "title", "created": "created", "id": "id"}

// Album/query takes a filter with two properties, both optional: slug, the
// album's slug, and photoId, the id of a photo it must hold.  With no sort,
// albums come in order of slug.
func (s *Server) albumQuery(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseQuery(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Query(c, s.begin, limits, std.Querier[*txn]{
		Type: store.TypeAlbum,
		All: func(ctx context.Context, tx *txn, req *std.QueryRequest) ([]string, error) {
			var filter struct {
				Slug    *string `json:"slug"`
				PhotoID *string `json:"photoId"`
			}
			if req.Filter != nil {
				if err := jmap.DecodeArgs(req.Filter, &filter); err != nil {
					return nil, jmap.Errorf(jmap.ErrUnsupportedFilter, "%v", err)
				}
			}
			var where []string
			var args []any
			if filter.Slug != nil {
				where, args = append(where, `slug = ?`), append(args, *filter.Slug)
			}
			if filter.PhotoID != nil {
				where = append(where, `id IN (SELECT album_id FROM album_photo WHERE photo_id = ?)`)
				args = append(args, *filter.PhotoID)
			}

			comparators, err := req.Comparators()
			if err != nil {
				return nil, err
			}
			if len(comparators) == 0 {
				comparators = []std.Comparator{{Property: "slug"}}
			}
			order, err := orderBy(albumSorts, comparators, store.TypeAlbum)
			if err != nil {
				return nil, err
			}

			sql := `SELECT id FROM album`
			if len(where) > 0 {
				sql += ` WHERE ` + strings.Join(where, ` AND `)
			}
			return idsOf(ctx, tx, sql+` ORDER BY `+order, args...)
		},
	}, req)
}

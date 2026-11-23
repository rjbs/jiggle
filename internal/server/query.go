package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"

	"github.com/rjbs/jiggle/internal/site"
	"github.com/rjbs/jiggle/internal/store"
)

// photoSearch is a jiggle query, as conditions on the photo table.
type photoSearch struct {
	where []string
	args  []any

	// album is the id of the first album the query names, whose order is
	// the order of the results, or empty.
	album string

	// limit is the most photos wanted, or zero for all of them.
	limit int
}

var (
	yearRE  = regexp.MustCompile(`\A[0-9]{4}\z`)
	limitRE = regexp.MustCompile(`\A[1-9][0-9]*\z`)
)

// parsePhotoSearch reads a jiggle query: terms, with spaces between, all of
// which a photo must match.
//
//	pending         not yet reviewed (and so not published)
//	private         private
//	public          public: published and listed
//	unlisted        unlisted: published, but listed only in albums
//	all             every photo
//	album:SLUG      in the album with that slug
//	tag:TAG         tagged TAG, compared as the site compares tags (by slug)
//	year:YYYY       taken in that year
//	id:ID           the photo with that id; given more than once, any of them
//	limit:N         only the first N of the photos that match
//
// A query with no terms matches every photo.  An unknown term is
// unsupportedFilter, and an album that doesn't exist is invalidArguments;
// neither is an empty result.
func parsePhotoSearch(ctx context.Context, tx *txn, query string) (*photoSearch, error) {
	search := &photoSearch{}
	var ids []string

	for _, term := range strings.Fields(query) {
		name, value, _ := strings.Cut(term, ":")
		switch {
		case term == "all":

		case term == "pending" || term == "private" || term == "public" || term == "unlisted":
			search.where = append(search.where, `visibility = ?`)
			search.args = append(search.args, term)

		case name == "album" && value != "":
			var album string
			err := tx.QueryRowContext(ctx, `SELECT id FROM album WHERE slug = ?`, value).Scan(&album)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, jmap.ArgError("no album named %s", value)
			}
			if err != nil {
				return nil, err
			}
			if search.album == "" {
				search.album = album
			}
			search.where = append(search.where, `id IN (SELECT photo_id FROM album_photo WHERE album_id = ?)`)
			search.args = append(search.args, album)

		case name == "tag" && value != "":
			tags, err := tagsWithSlug(ctx, tx, site.TagSlug(value))
			if err != nil {
				return nil, err
			}
			search.where = append(search.where, `id IN (SELECT photo_id FROM photo_tag WHERE `+among("tag")+`)`)
			search.args = append(search.args, idList(tags))

		case name == "year" && yearRE.MatchString(value):
			search.where = append(search.where, `substr(taken, 1, 5) = ?`)
			search.args = append(search.args, value+"-")

		case name == "id" && value != "":
			ids = append(ids, value)

		case name == "limit" && limitRE.MatchString(value):
			if search.limit != 0 {
				return nil, jmap.ArgError("limit given more than once")
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, jmap.ArgError("limit:%s is too many", value)
			}
			search.limit = n

		default:
			return nil, jmap.Errorf(jmap.ErrUnsupportedFilter, "unknown query term: %s", term)
		}
	}

	if ids != nil {
		search.where = append(search.where, among("id"))
		search.args = append(search.args, idList(ids))
	}
	return search, nil
}

// tagsWithSlug returns the tags in use that the site would show as the tag
// with the given slug.
func tagsWithSlug(ctx context.Context, tx *txn, slug string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT tag FROM photo_tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		if site.TagSlug(tag) == slug {
			tags = append(tags, tag)
		}
	}
	return tags, rows.Err()
}

// The columns a photo can be sorted by, by property.  They are compared as
// text, so times with different UTC offsets are not in the order they
// happened in; taken, which often has no offset at all, is the time on the
// clock where the photo was taken.
var photoSorts = map[string]string{"taken": "taken", "added": "added", "id": "id"}

// Photo/query takes a filter with one property, query, a jiggle query; see
// parsePhotoSearch.  With no sort, photos come in album order when the
// query names an album, and otherwise in the order they were taken, oldest
// first, with undated photos last.  A limit: term in the query counts from
// the start of that order, whatever page of it is then asked for.
func (s *Server) photoQuery(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseQuery(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Query(c, s.begin, limits, std.Querier[*txn]{
		Type: store.TypePhoto,
		All: func(ctx context.Context, tx *txn, req *std.QueryRequest) ([]string, error) {
			var filter struct {
				Query string `json:"query"`
			}
			if req.Filter != nil {
				if err := jmap.DecodeArgs(req.Filter, &filter); err != nil {
					return nil, jmap.Errorf(jmap.ErrUnsupportedFilter, "%v", err)
				}
			}
			search, err := parsePhotoSearch(ctx, tx, filter.Query)
			if err != nil {
				return nil, err
			}

			comparators, err := req.Comparators()
			if err != nil {
				return nil, err
			}
			order, err := orderBy(photoSorts, comparators, store.TypePhoto)
			if err != nil {
				return nil, err
			}
			args := search.args
			switch {
			case len(comparators) > 0:
			case search.album != "":
				order = `(SELECT position FROM album_photo WHERE album_id = ? AND photo_id = photo.id)`
				args = append(args, search.album)
			default:
				order = `taken IS NULL, taken, id`
			}

			sql := `SELECT id FROM photo`
			if len(search.where) > 0 {
				sql += ` WHERE ` + strings.Join(search.where, ` AND `)
			}
			sql += ` ORDER BY ` + order
			if search.limit > 0 {
				sql += ` LIMIT ` + strconv.Itoa(search.limit)
			}
			return idsOf(ctx, tx, sql, args...)
		},
	}, req)
}

// idsOf runs a query that selects ids, and returns them.
func idsOf(ctx context.Context, tx *txn, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

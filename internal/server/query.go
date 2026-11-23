package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"

	"github.com/rjbs/jiggle/internal/site"
	"github.com/rjbs/jiggle/internal/store"
)

// A condition is a condition in SQL, with the values for its placeholders.
type condition struct {
	sql  string
	args []any
}

// compileFilter turns a filter (RFC 8620 §5.5) into a condition.  A filter
// is a FilterCondition, which is the type's own to make sense of, by way of
// leaf, or a FilterOperator, which joins filters with AND, OR or NOT.  A
// nil filter is a condition that everything meets.
func compileFilter(raw json.RawMessage, leaf func(json.RawMessage) (*condition, error)) (*condition, error) {
	if raw == nil {
		return &condition{sql: "1"}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, jmap.ArgError("a filter must be an object")
	}
	if _, ok := fields["operator"]; !ok {
		return leaf(raw)
	}

	var node struct {
		Operator   string            `json:"operator"`
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := jmap.DecodeArgs(raw, &node); err != nil {
		return nil, err
	}
	join, empty, not := " AND ", "1", false
	switch node.Operator {
	case "AND":
	case "OR":
		join, empty = " OR ", "0"
	case "NOT":
		// NOT is met by what meets none of its conditions.
		join, empty, not = " OR ", "0", true
	default:
		return nil, jmap.Errorf(jmap.ErrUnsupportedFilter, "unknown filter operator %q", node.Operator)
	}

	out := &condition{sql: empty}
	var parts []string
	for _, child := range node.Conditions {
		c, err := compileFilter(child, leaf)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "("+c.sql+")")
		out.args = append(out.args, c.args...)
	}
	if len(parts) > 0 {
		out.sql = strings.Join(parts, join)
	}
	if not {
		out.sql = "NOT (" + out.sql + ")"
	}
	return out, nil
}

// all joins conditions that must all be met.
func all(conditions []condition) *condition {
	out := &condition{sql: "1"}
	var parts []string
	for _, c := range conditions {
		parts = append(parts, c.sql)
		out.args = append(out.args, c.args...)
	}
	if len(parts) > 0 {
		out.sql = strings.Join(parts, " AND ")
	}
	return out
}

// photoCondition makes a condition of a Photo FilterCondition, whose
// properties are:
//
//	visibility   the photo's visibility
//	inAlbum      the id of an album the photo is in
//	tag          a tag the photo has, compared as the site compares tags,
//	             by slug, so that "High St" finds photos tagged "high-st"
//	takenBefore  the photo was taken before this time
//	takenAfter   the photo was taken at this time or after it
//
// Times are compared as text, and may be cut short: a photo is taken after
// "2026" and before "2027" if it was taken in 2026.  A photo with no date
// is taken neither before nor after anything.
func photoCondition(ctx context.Context, tx *txn, raw json.RawMessage) (*condition, error) {
	var filter struct {
		Visibility  *string `json:"visibility"`
		InAlbum     *string `json:"inAlbum"`
		Tag         *string `json:"tag"`
		TakenBefore *string `json:"takenBefore"`
		TakenAfter  *string `json:"takenAfter"`
	}
	if err := jmap.DecodeArgs(raw, &filter); err != nil {
		return nil, jmap.Errorf(jmap.ErrUnsupportedFilter, "%v", err)
	}

	var conditions []condition
	if filter.Visibility != nil {
		conditions = append(conditions, condition{`visibility = ?`, []any{*filter.Visibility}})
	}
	if filter.InAlbum != nil {
		conditions = append(conditions, condition{
			`id IN (SELECT photo_id FROM album_photo WHERE album_id = ?)`, []any{*filter.InAlbum}})
	}
	if filter.Tag != nil {
		tags, err := tagsWithSlug(ctx, tx, site.TagSlug(*filter.Tag))
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, condition{
			`id IN (SELECT photo_id FROM photo_tag WHERE ` + among("tag") + `)`, []any{idList(tags)}})
	}
	if filter.TakenBefore != nil {
		conditions = append(conditions, condition{`taken < ?`, []any{*filter.TakenBefore}})
	}
	if filter.TakenAfter != nil {
		conditions = append(conditions, condition{`taken >= ?`, []any{*filter.TakenAfter}})
	}
	return all(conditions), nil
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

// A sortBy is something a type can be sorted by: an expression in SQL,
// with the values for its placeholders.
type sortBy = condition

// orderBy makes an ORDER BY of comparators.  Whatever has no value sorts
// last, either way up, and objects that are otherwise equal are in order
// of id.
func orderBy(terms []sortBy, ascending []bool) *condition {
	out := &condition{}
	var parts []string
	for i, term := range terms {
		direction := "ASC"
		if !ascending[i] {
			direction = "DESC"
		}
		parts = append(parts, term.sql+" IS NULL", term.sql+" "+direction)
		out.args = append(out.args, term.args...)
		out.args = append(out.args, term.args...)
	}
	out.sql = strings.Join(append(parts, "id"), ", ")
	return out
}

// photoOrder makes an ORDER BY of the comparators of a Photo/query, whose
// properties are:
//
//	taken     when the photo was taken
//	added     when the photo was added
//	id        the photo's id
//	position  the photo's place in an album, whose id the comparator gives
//	          as inAlbum; photos not in the album come last
//
// Times are compared as text, so two with different UTC offsets are not in
// the order they happened in; taken, which often has no offset at all, is
// the time on the clock where the photo was taken.  With no comparators,
// photos are in the order they were taken, with undated photos last.
func photoOrder(sort []json.RawMessage) (*condition, error) {
	if len(sort) == 0 {
		return orderBy([]sortBy{{sql: "taken"}}, []bool{true}), nil
	}
	var terms []sortBy
	var ascending []bool
	for _, raw := range sort {
		var cmp struct {
			std.Comparator
			InAlbum *string `json:"inAlbum"`
		}
		if err := jmap.DecodeArgs(raw, &cmp); err != nil {
			return nil, err
		}
		if (cmp.Property == "position") != (cmp.InAlbum != nil) {
			return nil, jmap.ArgError("inAlbum is for sorting by position, which needs it")
		}
		switch cmp.Property {
		case "taken", "added", "id":
			terms = append(terms, sortBy{sql: cmp.Property})
		case "position":
			terms = append(terms, sortBy{
				`(SELECT position FROM album_photo WHERE album_id = ? AND photo_id = photo.id)`, []any{*cmp.InAlbum}})
		default:
			return nil, jmap.Errorf(jmap.ErrUnsupportedSort, "a Photo can't be sorted by %q", cmp.Property)
		}
		ascending = append(ascending, cmp.Ascending())
	}
	return orderBy(terms, ascending), nil
}

// Photo/query takes the filters of photoCondition and the comparators of
// photoOrder.
func (s *Server) photoQuery(c *jmap.Call, raw json.RawMessage) (any, error) {
	req, err := std.ParseQuery(raw, nil)
	if err != nil {
		return nil, err
	}
	return std.Query(c, s.begin, limits, std.Querier[*txn]{
		Type: store.TypePhoto,
		All: func(ctx context.Context, tx *txn, req *std.QueryRequest) ([]string, error) {
			where, err := compileFilter(req.Filter, func(raw json.RawMessage) (*condition, error) {
				return photoCondition(ctx, tx, raw)
			})
			if err != nil {
				return nil, err
			}
			order, err := photoOrder(req.Sort)
			if err != nil {
				return nil, err
			}
			return idsOf(ctx, tx, `SELECT id FROM photo WHERE `+where.sql+` ORDER BY `+order.sql,
				append(where.args, order.args...)...)
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

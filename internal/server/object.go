package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/rjbs/gizmo/jmap"
)

// object is an object as a client sees it, by property.
type object = map[string]any

// generic returns the object as it would be once decoded from JSON, which
// is the shape a patch has and so the shape a patch can be applied to.
func generic(o object) object {
	raw, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	out, err := jmap.DecodeObject(raw)
	if err != nil {
		panic(err)
	}
	return out
}

// same reports whether two values are the same JSON.
func same(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// patched returns the object with a /set update's patch applied to it, in
// the shape of generic.
func patched(old object, raw json.RawMessage) (object, error) {
	patch, err := jmap.DecodeObject(raw)
	if err != nil {
		return nil, &jmap.SetError{Type: jmap.SetErrInvalidPatch, Description: err.Error()}
	}
	now := generic(old)
	if serr := jmap.ApplyPatch(now, patch); serr != nil {
		return nil, serr
	}
	return now, nil
}

// checker collects the properties of an object that can't be as a client
// asked, so that all of them can be reported at once.
type checker struct {
	props object
	bad   map[string]bool
}

func check(props object) *checker { return &checker{props: props, bad: map[string]bool{}} }

func (c *checker) fail(name string) { c.bad[name] = true }

// only fails every property that isn't among the names.
func (c *checker) only(names []string) {
	for name := range c.props {
		found := false
		for _, n := range names {
			found = found || n == name
		}
		if !found {
			c.fail(name)
		}
	}
}

// unchanged fails each of the named properties that isn't as it is in old,
// where a property that is missing is the same as one that is null.  They
// are the properties that only the server sets.
func (c *checker) unchanged(old object, names ...string) {
	for _, name := range names {
		if !same(c.props[name], old[name]) {
			c.fail(name)
		}
	}
}

// text returns a property that must be a string.
func (c *checker) text(name string) string {
	s, ok := c.props[name].(string)
	if !ok {
		c.fail(name)
	}
	return s
}

// optionalText returns a property that must be a string or null.
func (c *checker) optionalText(name string) *string {
	switch v := c.props[name].(type) {
	case nil:
	case string:
		return &v
	default:
		c.fail(name)
	}
	return nil
}

// err is nil, or the invalidProperties error for what has failed.
func (c *checker) err() error {
	if len(c.bad) == 0 {
		return nil
	}
	names := make([]string, 0, len(c.bad))
	for name := range c.bad {
		names = append(names, name)
	}
	sort.Strings(names)
	return jmap.InvalidProperties("these properties can't be set, or not to that", names...)
}

// told returns what a client that asked for one object must be told of the
// object it got: each of the named properties that isn't what it asked for.
func told(asked, got object, names []string) object {
	var out object
	for _, name := range names {
		if !same(asked[name], got[name]) {
			if out == nil {
				out = object{}
			}
			out[name] = got[name]
		}
	}
	return out
}

// number returns a JSON number as a float64, if that is what v is.
func number(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// idList is a list of ids as a JSON array, for a query to take apart with
// json_each.  An id that is all digits stays text that way, where a bound
// parameter for each would be at the mercy of SQLite's limit on them.
func idList(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	raw, _ := json.Marshal(ids)
	return string(raw)
}

// among is a condition that the column is one of the ids of an idList, for
// which it has one placeholder.
func among(column string) string {
	return column + ` IN (SELECT value FROM json_each(?))`
}

func nullText(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

func nullFloat(n sql.NullFloat64) any {
	if !n.Valid {
		return nil
	}
	return n.Float64
}

package site

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// toJSON makes JSON as Perl jiggle makes it, so that a file that hasn't
// changed is the same bytes: keys in order, no spaces, and no character
// escaped that needn't be.  Go escapes <, > and & unless told not to, and
// the two line separators regardless, which are put back.
func toJSON(v any) []byte {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Everything given to this is maps, slices, strings and numbers.
		panic(err)
	}
	data := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	for _, separator := range []rune{0x2028, 0x2029} {
		escaped := fmt.Sprintf(`\u%04x`, separator)
		data = bytes.ReplaceAll(data, []byte(escaped), []byte(string(separator)))
	}
	return data
}

// number is a number that isn't a whole number by nature, like a
// coordinate.  Perl jiggle writes one that happens to be whole with a ".0",
// as 40.0, where Go would write 40.
type number float64

func (n number) MarshalJSON() ([]byte, error) {
	text := strconv.FormatFloat(float64(n), 'g', 15, 64)
	if !strings.ContainsAny(text, ".e") {
		text += ".0"
	}
	return []byte(text), nil
}

// toJSONText is toJSON as text, for a template to put in an attribute.
func toJSONText(v any) string { return string(toJSON(v)) }

// embedData returns what another site needs to embed the photo, which is
// published as /p/ID/embed.json: its title, its page's URL, its type, and
// the URL and size of each rendition useful for embedding (and, for a
// video, of the video).  URLs are absolute.  It includes no location, and
// only public and unlisted photos are published at all, so a private or
// unknown id is simply a 404.
//
// format is the version of this structure, for the blog plugin to check.
func (s *Site) embedData(p *Photo) map[string]any {
	rendition := func(name string) *media {
		r := p.Renditions[name]
		return mediaOf(s.Absolute(RenditionURL(p, name)), r)
	}

	data := map[string]any{
		"format": 1,
		"id":     p.ID,
		"type":   p.Type,
		"title":  p.Title,
		"alt":    DisplayTitle(p),
		"taken":  nil,
		"url":    s.Absolute(PhotoURL(p)),
		"width":  p.Width,
		"height": p.Height,
		"renditions": map[string]*media{
			"500.webp":  rendition("500.webp"),
			"1024.webp": rendition("1024.webp"),
			"2048.webp": rendition("2048.webp"),
		},
		"video": nil,
	}
	if p.Taken != "" {
		data["taken"] = p.Taken
	}
	if p.IsVideo() {
		data["video"] = rendition("video.mp4")
	}
	return data
}

// albumData returns what the full-screen viewer needs to show an album,
// which is published as /albums/SLUG/photos.json: the album's title, and
// its photos in order, each as from viewerEntry.
//
// format is the version of this structure, for the viewer to check.
func (s *Site) albumData(a *Album) map[string]any {
	photos := make([]map[string]any, len(a.Photos))
	for i, p := range a.Photos {
		photos[i] = s.viewerEntry(a, p)
	}
	return map[string]any{"format": 1, "slug": a.Slug, "title": a.Title, "photos": photos}
}

// viewerEntry returns one photo as the viewer shows it: its id, the URL of
// its page within the album, its display title, the src and srcset of its
// image (for a video, of its poster) with the size of the largest, and, for
// a video, the URL and size of the video.  URLs are site-relative.
func (s *Site) viewerEntry(a *Album, p *Photo) map[string]any {
	large := p.Renditions["2048.webp"]
	entry := map[string]any{
		"id":     p.ID,
		"url":    AlbumPhotoURL(a, p),
		"title":  DisplayTitle(p),
		"width":  large.Width,
		"height": large.Height,
		"src":    RenditionURL(p, "1024.webp"),
		"srcset": Srcset(p),
	}
	if p.IsVideo() {
		video := p.Renditions["video.mp4"]
		entry["video"] = mediaOf(RenditionURL(p, "video.mp4"), video)
	}
	return entry
}

// geoJSON is the map's points: every listed photo with a public location.
func (s *Site) geoJSON() map[string]any {
	features := []map[string]any{}
	for _, p := range s.Listed {
		loc := s.PublicLocation(p)
		if loc == nil {
			continue
		}
		features = append(features, map[string]any{
			"type":     "Feature",
			"geometry": map[string]any{"type": "Point", "coordinates": []number{number(loc.Lon), number(loc.Lat)}},
			"properties": map[string]any{
				"title": DisplayTitle(p),
				"url":   PhotoURL(p),
				"thumb": RenditionURL(p, "h480.webp"),
			},
		})
	}
	return map[string]any{"type": "FeatureCollection", "features": features}
}

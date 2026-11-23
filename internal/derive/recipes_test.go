package derive

import (
	"reflect"
	"testing"
)

func recipe(t *testing.T, name string) Recipe {
	t.Helper()
	for _, r := range Recipes {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no recipe named %s", name)
	return Recipe{}
}

func sizeIs(t *testing.T, desc string, w, h int64, name string, wantW, wantH int64) {
	t.Helper()
	gotW, gotH := recipe(t, name).Size(w, h)
	if gotW != wantW || gotH != wantH {
		t.Errorf("%s: %s of %dx%d is %dx%d, want %dx%d", desc, name, w, h, gotW, gotH, wantW, wantH)
	}
}

// The first cases are those of Perl jiggle's t/photo.t, and must agree
// with it.
func TestSize(t *testing.T) {
	sizeIs(t, "landscape box", 4000, 3000, "1024.webp", 1024, 768)
	sizeIs(t, "portrait box", 3000, 4000, "2048.webp", 1536, 2048)
	sizeIs(t, "never enlarged", 800, 600, "2048.webp", 800, 600)
	sizeIs(t, "rounds to nearest", 5712, 4284, "500.webp", 500, 375)
	sizeIs(t, "row, landscape", 5712, 4284, "h480.webp", 640, 480)
	sizeIs(t, "row, portrait", 4284, 5712, "h480.webp", 360, 480)
	sizeIs(t, "row, panorama", 10000, 1000, "h480.webp", 1920, 192)

	sizeIs(t, "a poster is the size of its video", 1920, 1080, "poster.png", 1920, 1080)
	sizeIs(t, "video", 3840, 2160, "video.mp4", 1920, 1080)
	sizeIs(t, "video rounds to even", 1000, 563, "video.mp4", 1000, 564)
	sizeIs(t, "small video", 640, 480, "video.mp4", 640, 480)
}

func namesFor(t *testing.T, mediaType string, want ...string) {
	t.Helper()
	var got []string
	for _, r := range RecipesFor(mediaType) {
		got = append(got, r.Name+"@"+r.VersionFor(mediaType))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recipes for a %s\n got: %q\nwant: %q", mediaType, got, want)
	}
}

func TestRecipesFor(t *testing.T) {
	namesFor(t, "photo", "h480.webp@1", "500.webp@1", "1024.webp@1", "2048.webp@1", "og.jpg@1")
	namesFor(t, "video", "poster.png@1", "h480.webp@1+poster1", "500.webp@1+poster1",
		"1024.webp@1+poster1", "2048.webp@1+poster1", "og.jpg@1+poster1", "video.mp4@1")
}

func TestBlobID(t *testing.T) {
	const original = "3f9a0c21b7e40000000000000000000000000000000000000000000000000000"
	id := BlobID(original, "1024.webp", "1", 0)
	if len(id) != 41 || id[0] != 'r' {
		t.Errorf("a rendition's blobId is %q", id)
	}
	if id != BlobID(original, "1024.webp", "1", 0) {
		t.Errorf("a rendition's blobId changes from one call to the next")
	}
	for desc, other := range map[string]string{
		"name":     BlobID(original, "2048.webp", "1", 0),
		"version":  BlobID(original, "1024.webp", "2", 0),
		"rotate":   BlobID(original, "1024.webp", "1", 90),
		"original": BlobID("4"+original[1:], "1024.webp", "1", 0),
	} {
		if other == id {
			t.Errorf("a rendition's blobId doesn't depend on its %s", desc)
		}
	}
}

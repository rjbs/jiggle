package derive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// needs skips the test unless the programs are there to run.
func needs(t *testing.T, programs ...string) {
	t.Helper()
	for _, p := range programs {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("no %s", p)
		}
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}

// jpeg makes a JPEG of the given size, with the given tags set by exiftool,
// like "-Orientation#=6", and returns its path.
func jpeg(t *testing.T, width, height string, tags ...string) string {
	t.Helper()
	needs(t, "vips", "vipsheader", "exiftool")
	path := filepath.Join(t.TempDir(), "photo.jpg")
	run(t, "vips", "black", path, width, height, "--bands", "3")
	if len(tags) > 0 {
		run(t, "exiftool", append(append([]string{"-q", "-overwrite_original"}, tags...), path)...)
	}
	return path
}

// clip makes a two-second video, 320 by 240, with sound, and returns its
// path.
func clip(t *testing.T, name string, options ...string) string {
	t.Helper()
	needs(t, "ffmpeg", "ffprobe", "vips", "vipsheader", "exiftool")
	path := filepath.Join(t.TempDir(), name)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2", "-pix_fmt", "yuv420p"}
	run(t, "ffmpeg", append(append(args, options...), path)...)
	return path
}

func facts(t *testing.T, path string) *Facts {
	t.Helper()
	f, err := ReadFacts(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFactsOfAPhoto(t *testing.T) {
	f := facts(t, jpeg(t, "400", "300",
		"-Orientation#=6", "-DateTimeOriginal=2026:07:17 17:23:17", "-OffsetTimeOriginal=+02:00",
		"-GPSLatitude=33.5", "-GPSLatitudeRef=S", "-GPSLongitude=151.25", "-GPSLongitudeRef=E"))

	if f.FileType != "JPEG" || !f.Supported || f.Media != (Media{"photo", "jpg"}) {
		t.Errorf("it is %q, %+v, supported %v", f.FileType, f.Media, f.Supported)
	}
	if f.Width != 300 || f.Height != 400 {
		t.Errorf("turned upright it is %d by %d, want 300 by 400", f.Width, f.Height)
	}
	if f.Taken != "2026-07-17T17:23:17+02:00" {
		t.Errorf("taken %q", f.Taken)
	}
	if f.Location == nil || *f.Location != (Location{-33.5, 151.25}) {
		t.Errorf("location %+v, want -33.5, 151.25", f.Location)
	}
	if f.Duration != nil || f.LivePhoto {
		t.Errorf("a photo has a duration, or is live")
	}
}

func TestFactsOfABarePhoto(t *testing.T) {
	f := facts(t, jpeg(t, "400", "300", "-CreateDate=2026:07:17 17:23:17", "-OffsetTime=nonsense"))
	if f.Width != 400 || f.Height != 300 || f.Taken != "2026-07-17T17:23:17" || f.Location != nil {
		t.Errorf("got %+v", f)
	}
	if bare := facts(t, jpeg(t, "40", "30")); bare.Taken != "" {
		t.Errorf("a photo with no date was taken %q", bare.Taken)
	}
}

func TestFactsOfAVideo(t *testing.T) {
	f := facts(t, clip(t, "clip.mov", "-metadata", "creation_time=2026-07-17T15:23:17Z"))
	if f.Media != (Media{"video", "mov"}) || !f.Supported {
		t.Errorf("it is %+v, supported %v", f.Media, f.Supported)
	}
	if f.Width != 320 || f.Height != 240 || f.Duration == nil || *f.Duration < 1.9 || *f.Duration > 2.1 {
		t.Errorf("got %+v", f)
	}
	if f.Taken != "2026-07-17T15:23:17Z" {
		t.Errorf("taken %q", f.Taken)
	}
}

func TestFactsOfSomethingElse(t *testing.T) {
	needs(t, "exiftool")
	path := filepath.Join(t.TempDir(), "notes.jpg")
	os.WriteFile(path, []byte("these are not the pixels you're looking for\n"), 0o644)
	if f := facts(t, path); f.Supported {
		t.Errorf("text is supported, as %+v", f.Media)
	}
}

// makesAll makes every rendition of a source, and requires that each be of
// the size its recipe says it will be, for a picture of the given size as
// it is shown.  It returns the directory they are in.
func makesAll(t *testing.T, src Source, width, height int64) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	for _, r := range RecipesFor(src.Type) {
		dest := filepath.Join(dir, r.Name)
		if _, err := r.Make(ctx, src, dest); err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		if r.Kind == Poster {
			src.Poster = dest
		}
		w, h, err := r.MadeSize(ctx, dest)
		if err != nil {
			t.Fatal(err)
		}
		if wantW, wantH := r.Size(width, height); w != wantW || h != wantH {
			t.Errorf("%s is %d by %d, want %d by %d", r.Name, w, h, wantW, wantH)
		}
	}
	return dir
}

func TestMakeAPhotosRenditions(t *testing.T) {
	big := jpeg(t, "3000", "2000")
	makesAll(t, Source{Type: "photo", Original: big}, 3000, 2000)
	makesAll(t, Source{Type: "photo", Original: big, Rotate: 90}, 2000, 3000)
	makesAll(t, Source{Type: "photo", Original: big, Rotate: 180}, 3000, 2000)
	makesAll(t, Source{Type: "photo", Original: jpeg(t, "300", "200")}, 300, 200)
	makesAll(t, Source{Type: "photo", Original: jpeg(t, "3000", "2000", "-Orientation#=6"), Rotate: 270}, 3000, 2000)
}

func TestRenditionsCarryNoLocation(t *testing.T) {
	original := jpeg(t, "3000", "2000", "-GPSLatitude=33.5", "-GPSLatitudeRef=S", "-GPSLongitude=151.25", "-GPSLongitudeRef=E")
	dir := makesAll(t, Source{Type: "photo", Original: original}, 3000, 2000)
	for _, name := range []string{"og.jpg", "2048.webp"} {
		if f := facts(t, filepath.Join(dir, name)); f.Location != nil {
			t.Errorf("%s says where it was taken: %+v", name, f.Location)
		}
	}
}

func TestMakeAVideosRenditions(t *testing.T) {
	video := clip(t, "clip.mov")
	makesAll(t, Source{Type: "video", Original: video, Duration: 2}, 320, 240)
	makesAll(t, Source{Type: "video", Original: video, Duration: 2, Rotate: 90}, 240, 320)
}

func TestMakeFromADamagedOriginal(t *testing.T) {
	needs(t, "vips")
	path := filepath.Join(t.TempDir(), "broken.jpg")
	os.WriteFile(path, []byte("not a JPEG"), 0o644)
	r := Recipes[2]
	_, err := r.Make(context.Background(), Source{Type: "photo", Original: path}, filepath.Join(t.TempDir(), r.Name))
	if err == nil || !strings.Contains(err.Error(), "vips") {
		t.Errorf("making an image of nonsense: %v", err)
	}
}

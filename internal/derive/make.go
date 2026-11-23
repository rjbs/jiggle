package derive

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Source is what renditions are made from.
type Source struct {
	// Type is "photo" or "video".
	Type string

	// Original is the original file.
	Original string

	// Poster is a video's poster, once it has been made, from which the
	// video's images are made.
	Poster string

	// Rotate is the turn clockwise, in degrees, to give the picture beyond
	// what the file itself calls for: 0, 90, 180 or 270.
	Rotate int

	// Duration is a video's length in seconds, if that is known.
	Duration float64
}

// Make makes the rendition of a recipe, as the file dest, whose extension
// must be that of the recipe's name: it is how libvips and ffmpeg choose
// what to write.
//
// Every image is turned upright, converted to sRGB, and stripped of its
// metadata, which is what keeps a photo's location out of what's published.
// A video's images are made from its poster, a frame from a moment in, and
// its video is H.264 and AAC, with only the first video and audio streams,
// no metadata, and its index at the front, so that it can begin to play
// before it has all arrived.
//
// Make returns what the programs it ran had to say, which is how libvips
// and ffmpeg report an original that is damaged but usable.
func (r Recipe) Make(ctx context.Context, src Source, dest string) (warnings string, err error) {
	var out bytes.Buffer
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(out.String()))
		}
		return nil
	}

	switch r.Kind {
	case Image:
		// A video's poster was turned as it was made, so the images
		// made from it are upright already.
		source, rotate := src.Original, src.Rotate
		if src.Type == "video" {
			source, rotate = src.Poster, 0
		}
		err = r.makeImage(run, source, dest, rotate)
	case Poster:
		err = makePoster(run, src, dest)
	case Video:
		err = r.makeVideo(run, src, dest)
	default:
		err = fmt.Errorf("unknown kind of recipe %q", r.Kind)
	}
	return strings.TrimSpace(out.String()), err
}

type runner func(name string, args ...string) error

func (r Recipe) makeImage(run runner, source, dest string, rotate int) error {
	w, h := r.Fit[0], r.Fit[1]
	out := fmt.Sprintf("%s[Q=%d,keep=none]", dest, r.Quality)
	thumbnail := func(to string, w, h int64) error {
		return run("vips", "thumbnail", source, to, strconv.FormatInt(w, 10), "--height", strconv.FormatInt(h, 10),
			"--size", "down", "--export-profile", "srgb")
	}
	if rotate == 0 {
		return thumbnail(out, w, h)
	}

	// With an extra rotation, shrink into the box as it will be after
	// turning (swapped for a quarter turn), into an uncompressed
	// intermediate, then turn it while encoding the rendition.
	if rotate%180 != 0 {
		w, h = h, w
	}
	tmp := filepath.Join(filepath.Dir(dest), ".turning-"+filepath.Base(dest)+".v")
	defer os.Remove(tmp)
	if err := thumbnail(tmp, w, h); err != nil {
		return err
	}
	return run("vips", "rot", tmp, out, fmt.Sprintf("d%d", rotate))
}

var ffmpeg = []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}

// turnFilter is the ffmpeg filters that turn a frame clockwise.
func turnFilter(rotate int) []string {
	switch rotate {
	case 90:
		return []string{"transpose=clock"}
	case 180:
		return []string{"hflip", "vflip"}
	case 270:
		return []string{"transpose=cclock"}
	}
	return nil
}

func makePoster(run runner, src Source, dest string) error {
	// A frame a moment in is more representative than the very first
	// one, which is often blurry or dark.  For very short clips, take the
	// middle.
	at := min(1, src.Duration/2)

	// ffmpeg applies the file's rotation while decoding, so the frame
	// comes out upright, which is what the image renditions expect.  Any
	// extra rotation is applied here, too.
	args := append(append([]string{}, ffmpeg...), "-ss", strconv.FormatFloat(at, 'f', -1, 64), "-i", src.Original)
	if turn := turnFilter(src.Rotate); turn != nil {
		args = append(args, "-vf", strings.Join(turn, ","))
	}
	return run("ffmpeg", append(args, "-frames:v", "1", "-update", "1", dest)...)
}

func (r Recipe) makeVideo(run runner, src Source, dest string) error {
	// Scaling happens after rotation, so these limits apply to the
	// upright frame.  H.264 needs even dimensions.
	filters := append(turnFilter(src.Rotate), fmt.Sprintf(
		"scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		r.Fit[0], r.Fit[1]))

	args := append(append([]string{}, ffmpeg...),
		"-i", src.Original,
		// Only the first video stream and the first audio stream, if
		// any.  iPhone files also carry spatial audio and timed
		// metadata tracks, which browsers can't use and which could
		// carry location.
		"-map", "0:v:0", "-map", "0:a:0?",
		"-vf", strings.Join(filters, ","),
		"-c:v", "libx264", "-preset", "slow", "-crf", "23",
		"-profile:v", "high", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2",
		"-map_metadata", "-1", "-map_chapters", "-1",
		"-movflags", "+faststart",
		dest)
	return run("ffmpeg", args...)
}

// ImageSize returns the width and height of an image file, in pixels.
func ImageSize(ctx context.Context, path string) (int64, int64, error) {
	size := func(field string) (int64, error) {
		out, err := exec.CommandContext(ctx, "vipsheader", "-f", field, path).Output()
		if err != nil {
			return 0, fmt.Errorf("vipsheader %s: %w", path, err)
		}
		return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	w, err := size("width")
	if err != nil {
		return 0, 0, err
	}
	h, err := size("height")
	return w, h, err
}

// VideoSize returns the width and height of a video file's picture, in
// pixels, as stored.
func VideoSize(ctx context.Context, path string) (int64, int64, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var w, h int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d,%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("ffprobe %s: %q is not a size", path, out)
	}
	return w, h, nil
}

// MadeSize returns the width and height of a rendition this recipe made.
func (r Recipe) MadeSize(ctx context.Context, path string) (int64, int64, error) {
	if r.Kind == Video {
		return VideoSize(ctx, path)
	}
	return ImageSize(ctx, path)
}

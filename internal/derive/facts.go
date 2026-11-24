package derive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Media is a kind of file that renditions can be made of.
type Media struct {
	// Type is "photo" or "video".
	Type string

	// Ext is the extension that files of the kind are kept under.
	Ext string
}

// mediaFor is the kinds of file there are recipes for, by the name
// exiftool gives each as its FileType.
var mediaFor = map[string]Media{
	"JPEG": {"photo", "jpg"},
	"PNG":  {"photo", "png"},
	"HEIC": {"photo", "heic"},
	"WEBP": {"photo", "webp"},
	"GIF":  {"photo", "gif"}, // an animated one becomes its first frame
	"MOV":  {"video", "mov"},
	"MP4":  {"video", "mp4"},
	"M4V":  {"video", "m4v"},
	"AVI":  {"video", "avi"}, // old cameras' clips; ffmpeg reads them fine
}

// MediaForExt returns the kind of file kept under an extension, if
// renditions can be made of it.
func MediaForExt(ext string) (Media, bool) {
	for _, m := range mediaFor {
		if m.Ext == ext {
			return m, true
		}
	}
	return Media{}, false
}

// Location is a place on the earth, in degrees.
type Location struct {
	Lat, Lon float64
}

// Facts are what a file says of itself.
type Facts struct {
	// FileType is what exiftool makes of the file, like "JPEG", or empty
	// if it makes nothing of it.
	FileType string

	// Media is the kind of file it is, and Supported whether renditions
	// can be made of that.
	Media     Media
	Supported bool

	// Width and Height are the picture's size as it is shown, turned
	// upright by its own orientation.
	Width, Height int64

	// Duration is a video's length in seconds.
	Duration *float64

	// Taken is when the picture was taken, as the time on the clock, like
	// 2026-07-17T17:23:17, with a UTC offset if the file has one.  It is
	// empty if the file doesn't say.
	Taken string

	Location *Location

	// LivePhoto is true of a video that is the moving part of a Live
	// Photo.
	LivePhoto bool
}

var exifTags = []string{
	"-FileType", "-ImageWidth", "-ImageHeight", "-Orientation",
	"-DateTimeOriginal", "-CreateDate", "-OffsetTimeOriginal", "-OffsetTime",
	// The GPS-group tags are unsigned; the Ref tags say which hemisphere.
	// The group is named because an unqualified GPSLatitude may be the
	// (signed) Composite tag instead, and negating that would flip it
	// back.
	"-GPS:GPSLatitude", "-GPS:GPSLongitude", "-GPSLatitudeRef", "-GPSLongitudeRef",
	"-Duration", "-Rotation", "-CreationDate", "-GPSCoordinates", "-ContentIdentifier",
}

// exif is a file's tags, as exiftool prints them with -n.
type exif map[string]any

func (e exif) text(name string) string {
	switch v := e[name].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

func (e exif) number(name string) (float64, bool) {
	n, err := strconv.ParseFloat(e.text(name), 64)
	return n, err == nil && e.text(name) != ""
}

func (e exif) whole(name string) int64 {
	n, _ := e.number(name)
	return int64(n)
}

var (
	exifTimeRE = regexp.MustCompile(`\A(\d{4}):(\d\d):(\d\d) (\d\d:\d\d:\d\d)`)
	qtTimeRE   = regexp.MustCompile(`\A(\d{4}):(\d\d):(\d\d) (\d\d:\d\d:\d\d)(?:\.\d+)?([-+]\d\d:\d\d)?`)
	offsetRE   = regexp.MustCompile(`\A[-+]\d\d:\d\d\z`)
)

// ReadFacts reads a file's facts, with exiftool.
func ReadFacts(ctx context.Context, path string) (*Facts, error) {
	args := append([]string{"-j", "-n"}, exifTags...)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "exiftool", append(args, path)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// exiftool exits with an error for a file it can't make out, having
	// said what it could, so its output decides.
	runErr := cmd.Run()

	var files []exif
	dec := json.NewDecoder(&stdout)
	dec.UseNumber()
	if err := dec.Decode(&files); err != nil || len(files) != 1 {
		if runErr != nil {
			return nil, fmt.Errorf("exiftool: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("exiftool said nothing of %s", path)
	}
	e := files[0]

	f := &Facts{FileType: e.text("FileType")}
	f.Media, f.Supported = mediaFor[f.FileType]
	if f.Media.Type == "video" {
		videoFacts(e, f)
	} else {
		imageFacts(e, f)
	}
	return f, nil
}

func imageFacts(e exif, f *Facts) {
	f.Width, f.Height = e.whole("ImageWidth"), e.whole("ImageHeight")
	// Orientations 5 to 8 are a quarter turn, with or without a flip.
	if e.whole("Orientation") >= 5 {
		f.Width, f.Height = f.Height, f.Width
	}

	when := e.text("DateTimeOriginal")
	if when == "" {
		when = e.text("CreateDate")
	}
	if m := exifTimeRE.FindStringSubmatch(when); m != nil {
		offset := e.text("OffsetTimeOriginal")
		if offset == "" {
			offset = e.text("OffsetTime")
		}
		if !offsetRE.MatchString(offset) {
			offset = ""
		}
		f.Taken = m[1] + "-" + m[2] + "-" + m[3] + "T" + m[4] + offset
	}

	lat, okLat := e.number("GPSLatitude")
	lon, okLon := e.number("GPSLongitude")
	if okLat && okLon {
		if e.text("GPSLatitudeRef") == "S" {
			lat = -lat
		}
		if e.text("GPSLongitudeRef") == "W" {
			lon = -lon
		}
		f.Location = &Location{lat, lon}
	}
}

func videoFacts(e exif, f *Facts) {
	if d, ok := e.number("Duration"); ok {
		f.Duration = &d
	}

	// The stored frame size is before rotation; a portrait clip from a
	// phone is stored as landscape with a 90-degree rotation.
	f.Width, f.Height = e.whole("ImageWidth"), e.whole("ImageHeight")
	if e.whole("Rotation")%180 != 0 {
		f.Width, f.Height = f.Height, f.Width
	}

	// QuickTime's CreationDate is local time with an offset.  CreateDate
	// is in UTC with no marker, so if it's all there is, it gets a Z.
	// Old cameras' AVI files carry an EXIF-style local time instead.
	switch {
	case e.text("CreationDate") != "":
		if m := qtTimeRE.FindStringSubmatch(e.text("CreationDate")); m != nil {
			f.Taken = m[1] + "-" + m[2] + "-" + m[3] + "T" + m[4] + m[5]
		}
	case e.text("CreateDate") != "":
		if m := exifTimeRE.FindStringSubmatch(e.text("CreateDate")); m != nil && m[1] != "0000" {
			f.Taken = m[1] + "-" + m[2] + "-" + m[3] + "T" + m[4] + "Z"
		}
	default:
		if m := exifTimeRE.FindStringSubmatch(e.text("DateTimeOriginal")); m != nil {
			f.Taken = m[1] + "-" + m[2] + "-" + m[3] + "T" + m[4]
		}
	}

	// With -n, GPSCoordinates is signed decimal "lat lon [alt]".
	if parts := strings.Fields(e.text("GPSCoordinates")); len(parts) >= 2 {
		lat, errLat := strconv.ParseFloat(parts[0], 64)
		lon, errLon := strconv.ParseFloat(parts[1], 64)
		if errLat == nil && errLon == nil {
			f.Location = &Location{lat, lon}
		}
	}

	f.LivePhoto = e.text("ContentIdentifier") != ""
}

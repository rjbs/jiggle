// Package derive knows the renditions made of each photo: their names,
// their recipes, and how large each comes out.
package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
)

// A Kind is what sort of thing a recipe makes.
type Kind string

const (
	// Poster is a frame of a video, from which its images are made.
	Poster Kind = "poster"
	Image  Kind = "image"
	Video  Kind = "video"
)

// Recipe is how one rendition is made.
type Recipe struct {
	// Name is the rendition's filename, which appears in published URLs.
	Name string

	// Version changes when the recipe does, so that what it made before
	// is made again.
	Version int

	Kind Kind

	// For limits a recipe to one type of media, "photo" or "video".  A
	// recipe with none applies to both.
	For string

	// Fit is the width and height of the box a rendition is shrunk to fit
	// inside.  It is never enlarged.  A recipe with no box keeps the
	// original's size.
	Fit [2]int64

	// Publish is false for an intermediate, which is kept but never part
	// of the site.
	Publish bool
}

// Recipes is every recipe, in the order they must be made: a video's poster
// comes before the images made from it.
//
// h480 is for the justified grids, where every photo in a row has the same
// height.  The width limit only matters for panoramas.
var Recipes = []Recipe{
	{Name: "poster.png", Version: 1, Kind: Poster, For: "video"},
	{Name: "h480.webp", Version: 1, Kind: Image, Fit: [2]int64{1920, 480}, Publish: true},
	{Name: "500.webp", Version: 1, Kind: Image, Fit: [2]int64{500, 500}, Publish: true},
	{Name: "1024.webp", Version: 1, Kind: Image, Fit: [2]int64{1024, 1024}, Publish: true},
	{Name: "2048.webp", Version: 1, Kind: Image, Fit: [2]int64{2048, 2048}, Publish: true},
	{Name: "og.jpg", Version: 1, Kind: Image, Fit: [2]int64{1200, 1200}, Publish: true},
	{Name: "video.mp4", Version: 1, Kind: Video, For: "video", Fit: [2]int64{1920, 1920}, Publish: true},
}

// RecipesFor returns the recipes that apply to a type of media, "photo" or
// "video", in the order they must be made.
func RecipesFor(mediaType string) []Recipe {
	var recipes []Recipe
	for _, r := range Recipes {
		if r.For == "" || r.For == mediaType {
			recipes = append(recipes, r)
		}
	}
	return recipes
}

// VersionFor is the version of everything that goes into the rendition this
// recipe makes of a type of media.  A video's images are made from its
// poster, so the poster's version is part of theirs.
func (r Recipe) VersionFor(mediaType string) string {
	version := fmt.Sprint(r.Version)
	if mediaType == "video" && r.Kind == Image {
		version += fmt.Sprintf("+poster%d", Recipes[0].Version)
	}
	return version
}

// Size returns the pixel dimensions of the rendition this recipe makes from
// a picture of the given width and height, which are the picture's as it is
// shown: upright, and after any extra turn.
func (r Recipe) Size(width, height int64) (int64, int64) {
	if r.Fit == [2]int64{} {
		return width, height
	}
	w, h := float64(width), float64(height)
	scale := min(1, float64(r.Fit[0])/w, float64(r.Fit[1])/h)

	// libvips rounds to nearest when it shrinks, and ffmpeg is told to
	// round to an even number.
	if r.Kind == Video {
		return 2 * int64(math.Floor(w*scale/2+0.5)), 2 * int64(math.Floor(h*scale/2+0.5))
	}
	return int64(math.Floor(w*scale + 0.5)), int64(math.Floor(h*scale + 0.5))
}

// BlobID is the blobId of a rendition: an "r", and then a digest of what
// decides its bytes, which are its original's blobId, its name, the version
// of its recipe, and the extra clockwise turn it was given, in degrees.
func BlobID(original, name, version string, rotate int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%d", original, name, version, rotate))
	return "r" + hex.EncodeToString(sum[:20])
}

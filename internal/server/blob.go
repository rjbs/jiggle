package server

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/rjbs/gizmo/blob"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// contentTypes are the content types of blobs, by the extension the blob
// table records.  Whatever isn't here is application/octet-stream.
var contentTypes = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif",
	"webp": "image/webp", "heic": "image/heic", "heif": "image/heif", "tif": "image/tiff", "tiff": "image/tiff",
	"dng": "image/x-adobe-dng", "avif": "image/avif",
	"mp4": "video/mp4", "m4v": "video/x-m4v", "mov": "video/quicktime", "avi": "video/x-msvideo",
	"3gp": "video/3gpp", "mts": "video/mp2t", "mpg": "video/mpeg", "webm": "video/webm",
}

// Get finds a blob for the download endpoint: an original or a rendition.
// It is half of what makes the server a blob.Store.
func (s *Server) Get(ctx context.Context, _, blobID string) (*blob.Blob, error) {
	var path, ext string
	err := s.store.DB.QueryRowContext(ctx, `SELECT path, ext FROM blob WHERE id = ?`, blobID).Scan(&path, &ext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, blob.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.cfg.Library, filepath.FromSlash(path)))
	if err != nil {
		// The database knows the blob and the library hasn't got it.
		s.cfg.Log.Error("blob has no file", "blob", blobID, "error", err)
		return nil, blob.ErrNotFound
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	contentType := contentTypes[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &blob.Blob{Content: f, Type: contentType, ModTime: info.ModTime()}, nil
}

// Put keeps an upload, as a blob that Photo/import can make a photo of.
// It is the other half of what makes the server a blob.Store.
//
// The blob's id is O and the SHA-256 of its bytes, so the same file
// uploaded twice is one blob.  What kind of file it is is judged here, by
// its content, and recorded as its extension; a file that isn't a photo or
// a video of a kind there are recipes for is kept too, with no extension,
// and is refused when it's imported.
func (s *Server) Put(ctx context.Context, up *blob.Upload) (*blob.Uploaded, error) {
	incoming := filepath.Join(s.cfg.Library, "originals", "incoming")
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		return nil, err
	}
	got, err := blob.Receive(incoming, up.Body)
	if err != nil {
		return nil, err
	}
	defer os.Remove(got.Path)

	id := store.OriginalBlobID(got.SHA256)
	uploaded := func(ext string) *blob.Uploaded {
		contentType := contentTypes[ext]
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		return &blob.Uploaded{BlobID: id, Type: contentType, Size: got.Size}
	}

	var ext string
	err = s.store.DB.QueryRowContext(ctx, `SELECT ext FROM blob WHERE id = ?`, id).Scan(&ext)
	if err == nil {
		return uploaded(ext), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// A file exiftool can make nothing of is just not a photo.
	if facts, err := derive.ReadFacts(ctx, got.Path); err == nil && facts.Supported {
		ext = facts.Media.Ext
	}
	path := "originals/" + got.SHA256[:2] + "/" + got.SHA256
	if ext != "" {
		path += "." + ext
	}
	full := filepath.Join(s.cfg.Library, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	if err := os.Chmod(got.Path, 0o444); err != nil {
		return nil, err
	}
	if err := os.Rename(got.Path, full); err != nil {
		return nil, err
	}
	// Two uploads of the same file at once both get here, with the same
	// file to show for it, and the second has no row to add.
	_, err = s.store.DB.ExecContext(ctx,
		`INSERT INTO blob (id, bytes, ext, path) VALUES (?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, id, got.Size, ext, path)
	if err != nil {
		return nil, err
	}
	return uploaded(ext), nil
}

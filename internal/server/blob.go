package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rjbs/gizmo/blob"
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

// Put is the other half, and refuses everything: nothing can be made of an
// upload until there is Photo/import.
func (s *Server) Put(context.Context, *blob.Upload) (*blob.Uploaded, error) {
	return nil, &blob.Refusal{Status: http.StatusNotImplemented, Detail: "this server doesn't take uploads yet"}
}

package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// What became of a file.
const (
	// Ingested: a photo was made of it.  Until Wait has been called, its
	// import may still be going on.
	Ingested = "ingested"
	// Exists: the library already has a photo of this file.
	Exists = "exists"
	// Skipped: the server won't make a photo of a file like this.
	Skipped = "skipped"
	// Failed: a photo was made, but the server couldn't import the file.
	Failed = "failed"
	// Discarded: the import failed, and the photo was then destroyed.
	Discarded = "discarded"
	// Unsent: the file couldn't be read, or uploaded, or imported, because
	// something went wrong on the way.
	Unsent = "unsent"
)

// A File is one file given to be ingested, and what became of it.
type File struct {
	Path   string
	Status string

	// ID is the photo: the new one, or the one that already had this
	// file.
	ID string

	// Reason is why the file was skipped, or failed, or went unsent.
	Reason string

	// Uploaded is whether the file's content was sent.  It isn't when the
	// server already has it.
	Uploaded bool
}

// An Ingest brings files into a library.
type Ingest struct {
	Client *Client

	// Log is called with a message for each file, and with progress.
	Log func(string)

	// Poll is how often Wait asks how the imports are going: a second, if
	// zero.
	Poll time.Duration
}

// Paths expands arguments into the files to ingest, in order of name: a
// file is itself, and a directory is the files in it whose names don't
// begin with a dot.  Directories inside a directory are left alone, and a file named twice
// is ingested once.
func Paths(args []string) ([]string, error) {
	var paths []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			paths = append(paths, arg)
			continue
		}
		entries, err := os.ReadDir(arg)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			// A link to a file counts as a file.
			if info, err := os.Stat(filepath.Join(arg, e.Name())); err == nil && info.Mode().IsRegular() {
				paths = append(paths, filepath.Join(arg, e.Name()))
			}
		}
	}
	sort.Strings(paths)
	return slices.Compact(paths), nil
}

// Files ingests each file, one after another, and returns what became of
// each, in the order given.  A file that can't be sent doesn't stop the
// rest; one that's Ingested has a photo, whose import the server has yet
// to do.  The error is only ctx's, if it was done before the files were.
func (in *Ingest) Files(ctx context.Context, paths []string) ([]File, error) {
	files := make([]File, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return files, err
		}
		f := File{Path: path}
		if err := in.file(ctx, &f); err != nil {
			f.Status, f.Reason = Unsent, err.Error()
		}
		switch f.Status {
		case Ingested:
			in.Log(fmt.Sprintf("ingest %s as %s", f.Path, f.ID))
		case Exists:
			in.Log(fmt.Sprintf("skip %s: already in library as %s", f.Path, f.ID))
		case Skipped:
			in.Log(fmt.Sprintf("skip %s: %s", f.Path, f.Reason))
		default:
			in.Log(fmt.Sprintf("can't ingest %s: %s", f.Path, f.Reason))
		}
		files = append(files, f)
	}
	return files, nil
}

// file ingests one file, and sets f's Status and what goes with it.
//
// The id of an upload is O and the SHA-256 of its bytes (see "Blobs" in
// doc/types.md), so the file's blobId is known before it's sent, and the
// import is tried first without sending it.  Only if the server has no
// such blob is the file uploaded.  That makes going over a camera's whole
// card again cheap, and it means an upload that was never imported, as
// when the last run was interrupted, isn't sent twice.
// -- claude, 2026-11-23
func (in *Ingest) file(ctx context.Context, f *File) error {
	source, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, source); err != nil {
		return err
	}

	photo := map[string]any{
		"blobId":   "O" + hex.EncodeToString(digest.Sum(nil)),
		"name":     filepath.Base(f.Path),
		"modified": info.ModTime().Format(time.RFC3339),
	}
	refused, err := in.imports(ctx, f, photo)
	if err != nil || refused != "blobNotFound" {
		return err
	}

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// The server says what the blob is, in case it should ever name
	// blobs some other way.
	up, err := in.Client.Upload(ctx, "application/octet-stream", source, info.Size())
	if err != nil {
		return fmt.Errorf("uploading: %w", err)
	}
	photo["blobId"] = up.BlobID
	f.Uploaded = true
	if refused, err = in.imports(ctx, f, photo); err == nil && refused == "blobNotFound" {
		err = fmt.Errorf("the server lost the upload: %s", f.Reason)
	}
	return err
}

// imports calls Photo/import for one photo and sets f by the answer.  It
// returns the type of the error the photo was refused with, if it was.
func (in *Ingest) imports(ctx context.Context, f *File, photo map[string]any) (string, error) {
	var answer struct {
		Created    map[string]struct{ ID string }
		NotCreated map[string]struct {
			Type        string
			Description string
			ExistingID  string `json:"existingId"`
		}
	}
	err := in.Client.Call(ctx, "Photo/import", map[string]any{"photos": map[string]any{"new": photo}}, &answer)
	if err != nil {
		return "", err
	}
	if made, ok := answer.Created["new"]; ok {
		f.Status, f.ID = Ingested, made.ID
		return "", nil
	}

	refused, ok := answer.NotCreated["new"]
	if !ok {
		return "", fmt.Errorf("Photo/import neither made a photo nor refused to")
	}
	f.Reason = refused.Description
	switch refused.Type {
	case "blobNotFound":
	case "alreadyExists":
		f.Status, f.ID = Exists, refused.ExistingID
	case "invalidPhoto":
		f.Status, f.Reason = Skipped, "not a photo or a video of a kind jiggle can use"
	default:
		return "", fmt.Errorf("Photo/import: %s: %s", refused.Type, refused.Description)
	}
	return refused.Type, nil
}

// each calls fn with the ids of the files that have the status, some
// hundreds at a time.
func each(files []File, status string, fn func(ids []string, byID map[string]*File) error) error {
	const most = 500
	ids, byID := []string{}, map[string]*File{}
	flush := func() error {
		if len(ids) == 0 {
			return nil
		}
		err := fn(ids, byID)
		ids, byID = []string{}, map[string]*File{}
		return err
	}
	for i := range files {
		if files[i].Status != status {
			continue
		}
		ids, byID[files[i].ID] = append(ids, files[i].ID), &files[i]
		if len(ids) == most {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// Wait waits until the server has done the import of every Ingested file,
// asking every Poll.  A file whose import failed becomes Failed, with the
// server's reason.  If ctx is done first, it returns its error, and the
// server carries on with the imports all the same.
func (in *Ingest) Wait(ctx context.Context, files []File) error {
	poll := in.Poll
	if poll == 0 {
		poll = time.Second
	}
	total := 0
	for _, f := range files {
		if f.Status == Ingested {
			total++
		}
	}
	if total == 0 {
		return nil
	}

	// Files whose imports are over are counted here, and aren't asked
	// about again.
	over := map[string]bool{}
	said, last := -1, time.Now()
	for {
		err := each(files, Ingested, func(ids []string, byID map[string]*File) error {
			var asking []string
			for _, id := range ids {
				if !over[id] {
					asking = append(asking, id)
				}
			}
			if len(asking) == 0 {
				return nil
			}
			var answer struct {
				List []struct {
					ID        string
					ImportJob *struct{ Error *string }
				}
				NotFound []string
			}
			args := map[string]any{"ids": asking, "properties": []string{"importJob"}}
			if err := in.Client.Call(ctx, "Photo/get", args, &answer); err != nil {
				return err
			}
			for _, p := range answer.List {
				switch {
				case p.ImportJob == nil:
					over[p.ID] = true
				case p.ImportJob.Error != nil:
					over[p.ID] = true
					byID[p.ID].Status, byID[p.ID].Reason = Failed, *p.ImportJob.Error
					in.Log(fmt.Sprintf("the import of %s failed: %s", byID[p.ID].Path, *p.ImportJob.Error))
				}
			}
			// Someone destroyed it while it was importing.
			for _, id := range answer.NotFound {
				over[id] = true
				byID[id].Status, byID[id].Reason = Failed, "the photo was destroyed"
			}
			return nil
		})
		if err != nil {
			return err
		}

		if len(over) == total {
			return nil
		}
		if len(over) != said && (said < 0 || time.Since(last) >= 10*time.Second) {
			in.Log(fmt.Sprintf("importing: %d of %d done", len(over), total))
			said, last = len(over), time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Discard destroys the photos of the Failed files, which have no original
// and no renditions, and makes them Discarded.  The uploads are left for
// the server to clear away.
func (in *Ingest) Discard(ctx context.Context, files []File) error {
	return each(files, Failed, func(ids []string, byID map[string]*File) error {
		var answer struct {
			Destroyed    []string
			NotDestroyed map[string]struct{ Type, Description string }
		}
		if err := in.Client.Call(ctx, "Photo/set", map[string]any{"destroy": ids}, &answer); err != nil {
			return err
		}
		for _, id := range answer.Destroyed {
			if f := byID[id]; f != nil {
				f.Status = Discarded
			}
		}
		for id, why := range answer.NotDestroyed {
			in.Log(fmt.Sprintf("can't discard %s: %s %s", id, why.Type, why.Description))
		}
		return nil
	})
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// The SetErrors of an import, beyond the standard ones.
const (
	// errBlobNotFound is for a blob there isn't.
	errBlobNotFound = "blobNotFound"

	// errInvalidPhoto is for a blob that is not a photo or a video, or
	// not of a kind that renditions can be made of.
	errInvalidPhoto = "invalidPhoto"
)

// importable checks that a blob can be made into the original of a photo,
// and returns what kind of file it is.  It must be an upload, of a kind
// that renditions can be made of, and no other photo's: neither its
// original nor what it is importing.  photoID is the photo it is for, or
// empty for one that doesn't exist yet.
func importable(ctx context.Context, tx *txn, blobID, photoID string) (derive.Media, error) {
	var ext string
	err := tx.QueryRowContext(ctx, `SELECT ext FROM blob WHERE id = ? AND id LIKE 'O%'`, blobID).Scan(&ext)
	if errors.Is(err, sql.ErrNoRows) {
		return derive.Media{}, &jmap.SetError{Type: errBlobNotFound, Description: "there is no upload " + blobID}
	}
	if err != nil {
		return derive.Media{}, err
	}
	media, ok := derive.MediaForExt(ext)
	if !ok {
		return derive.Media{}, &jmap.SetError{
			Type: errInvalidPhoto, Description: "the blob is not a photo or a video of a kind jiggle can use"}
	}

	var other string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM photo WHERE (blob_id = ?1 OR import_blob_id = ?1) AND id <> ?2`, blobID, photoID).Scan(&other)
	switch {
	case err == nil:
		return derive.Media{}, jmap.AlreadyExists("the blob is the original of another photo", other)
	case !errors.Is(err, sql.ErrNoRows):
		return derive.Media{}, err
	}
	return media, nil
}

// checkImport checks the import that an update would leave a photo with,
// and settles what it will be.  old is the photo as it is, and now the
// photo as it will be, which this may change.
//
// Setting importJob starts an import: of a new blob, to replace the
// photo's original; of the photo's own original, to read it again and
// remake its renditions; or of whatever the last import was of, to try
// again.  The client gives no error.  Setting it to null gives an import
// up, which only a photo with an original can do.
//
// A change to a photo's rotate starts an import of its own original, too,
// since every rendition has to be made again.
func checkImport(ctx context.Context, tx *txn, id string, old, now object) error {
	invalid := func(why string) error { return jmap.InvalidProperties(why, "importJob") }
	original, _ := old["blobId"].(string)
	job, _ := now["importJob"].(object)

	if same(old["importJob"], now["importJob"]) {
		// Renditions being made will be made again for the new turn by
		// the import that is making them.  A photo with no import
		// under way gets one.
		if same(old["rotate"], now["rotate"]) || job != nil || original == "" {
			return nil
		}
		facts := old["original"].(object)
		now["importJob"] = object{"blobId": original, "error": nil, "name": facts["name"], "modified": facts["modified"]}
		return nil
	}

	if job == nil {
		if original == "" {
			return invalid("a photo with no original can't give up its import; destroy it instead")
		}
		return nil
	}
	if job["error"] != nil {
		return invalid("an import's error is the server's to set")
	}
	blobID := job["blobId"].(string)
	media, err := importable(ctx, tx, blobID, id)
	if err != nil {
		return err
	}
	if media.Type != old["type"] {
		return invalid("a " + old["type"].(string) + " can't become a " + media.Type)
	}

	// Reading the original again keeps what is known of where it came
	// from, unless the client says otherwise.
	if blobID == original {
		facts := old["original"].(object)
		for _, name := range []string{"name", "modified"} {
			if job[name] == nil {
				job[name] = facts[name]
			}
		}
	}
	return nil
}

// Photo/import makes photos of uploaded blobs.  Its argument photos maps a
// creation id to what to make: blobId, the upload; name and modified, the
// name and modification time the file had, which an upload doesn't carry;
// and any property of a Photo that a client may set.  A new photo is
// pending unless it's given a visibility.
//
// It answers as a /set does, with created and notCreated.  A blob that is
// already a photo's is alreadyExists, with the photo's id; one there isn't
// is blobNotFound; and one that isn't a photo or video is invalidPhoto.
//
// A photo is made at once, with an importJob and no blobId.  Its original
// and its renditions come later, when the import has been done; see
// worker.go.
func (s *Server) photoImport(c *jmap.Call, raw json.RawMessage) (any, error) {
	var args struct {
		AccountID string                     `json:"accountId"`
		IfInState *string                    `json:"ifInState"`
		Photos    map[string]json.RawMessage `json:"photos"`
	}
	if err := jmap.DecodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if len(args.Photos) > limits.MaxObjectsInSet {
		return nil, jmap.Errorf(jmap.ErrRequestTooLarge, "at most %d photos per import", limits.MaxObjectsInSet)
	}
	cids := make([]string, 0, len(args.Photos))
	for cid := range args.Photos {
		cids = append(cids, cid)
	}
	sort.Strings(cids)

	defer s.kick()
	return std.Transact(c, s.begin, func(w *std.Work[*txn]) (any, error) {
		ctx := w.Context()
		oldState, err := w.Tx.State(ctx, store.TypePhoto)
		if err != nil {
			return nil, err
		}
		if args.IfInState != nil && *args.IfInState != oldState {
			return nil, jmap.Errorf(jmap.ErrStateMismatch, "the state is %s", oldState)
		}

		created, notCreated := map[string]any{}, map[string]any{}
		for _, cid := range cids {
			var photo object
			serr, err := w.Do(func() (err error) {
				photo, err = s.importPhoto(ctx, w.Tx, args.Photos[cid])
				return err
			})
			if err != nil {
				return nil, err
			}
			if serr != nil {
				notCreated[cid] = serr
				continue
			}
			created[cid] = photo
			c.SetCreatedID(cid, photo["id"].(string))
		}

		newState, err := w.Tx.State(ctx, store.TypePhoto)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"accountId": args.AccountID, "oldState": oldState, "newState": newState,
			"created": created, "notCreated": notCreated,
		}, nil
	})
}

// importPhoto makes one photo for Photo/import, and returns what the
// client wasn't the one to say of it, including its id.
func (s *Server) importPhoto(ctx context.Context, tx *txn, raw json.RawMessage) (object, error) {
	given, err := jmap.DecodeObject(raw)
	if err != nil {
		return nil, jmap.InvalidProperties(err.Error())
	}
	if _, has := given["importJob"]; has {
		return nil, jmap.InvalidProperties("an import is given as blobId, name and modified", "importJob")
	}

	// What the client leaves out is as it would be by default, and the
	// three properties of the import are the photo's importJob.
	asked := object{
		"title": "", "description": "", "taken": nil, "visibility": "pending",
		"rotate": json.Number("0"), "tags": map[string]any{}, "location": nil,
	}
	job := map[string]any{}
	for name, value := range given {
		switch name {
		case "blobId", "name", "modified":
			job[name] = value
		default:
			asked[name] = value
		}
	}
	asked["importJob"] = job
	now, err := checkPhoto(object{}, asked)
	if err != nil {
		// A fault in the import is a fault in the three properties it
		// was given as.
		var serr *jmap.SetError
		if errors.As(err, &serr) {
			for i, name := range serr.Properties {
				if name == "importJob" {
					serr.Properties = append(append(serr.Properties[:i:i], "blobId", "modified", "name"), serr.Properties[i+1:]...)
					break
				}
			}
		}
		return nil, err
	}
	job = now["importJob"].(object)
	media, err := importable(ctx, tx, job["blobId"].(string), "")
	if err != nil {
		return nil, err
	}

	id, err := tx.NewID(ctx, store.TypePhoto)
	if err != nil {
		return nil, err
	}
	modseq, err := tx.Create(ctx, store.TypePhoto, id)
	if err != nil {
		return nil, err
	}
	var lat, lon any
	private := false
	if location, ok := now["location"].(object); ok {
		lat, lon, private = location["lat"], location["lon"], location["isPrivate"].(bool)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO photo (id, type, title, description, taken, visibility, rotate, added,
		                   lat, lon, location_private, import_blob_id, import_name, import_mtime,
		                   created_modseq, modseq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, media.Type, now["title"], now["description"], now["taken"], now["visibility"], now["rotate"],
		s.cfg.Now().Format("2006-01-02T15:04:05Z07:00"),
		lat, lon, private, job["blobId"], job["name"], job["modified"], modseq, modseq)
	if err != nil {
		return nil, err
	}
	if err := writeTags(ctx, tx, id, nil, now["tags"].(map[string]bool)); err != nil {
		return nil, err
	}

	photos, _, err := loadPhotos(ctx, tx, []string{id})
	if err != nil {
		return nil, err
	}
	out := told(given, photos[id], photoProps)
	out["id"] = id
	return out, nil
}

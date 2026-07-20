package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func mediaFileFrom(m db.MediaFile) MediaFile {
	return MediaFile{
		ID:             m.ID,
		ContentID:      m.ContentID,
		LocalPath:      m.LocalPath,
		SHA256:         m.Sha256.String,
		TelegramFileID: m.TelegramFileID.String,
		Width:          int4Int(m.Width),
		Height:         int4Int(m.Height),
		Bytes:          int4Int(m.Bytes),
		CreatedAt:      tsTime(m.CreatedAt),
	}
}

// PutMediaFile upserts a media asset keyed on local_path (its stable
// on-disk identity; architecture §2.8). width/height/bytes are nil when
// unknown.
func (s *Store) PutMediaFile(ctx context.Context, contentID *uuid.UUID, localPath, sha256 string, width, height, bytes *int) (MediaFile, error) {
	m, err := s.q.PutMediaFile(ctx, db.PutMediaFileParams{
		ContentID: contentID,
		LocalPath: localPath,
		Sha256:    pgText(sha256),
		Width:     ptrInt4(width),
		Height:    ptrInt4(height),
		Bytes:     ptrInt4(bytes),
	})
	if err != nil {
		return MediaFile{}, err
	}
	return mediaFileFrom(m), nil
}

// GetMediaByContentID looks up a media file by its content_items link.
func (s *Store) GetMediaByContentID(ctx context.Context, contentID uuid.UUID) (MediaFile, bool, error) {
	m, err := s.q.GetMediaByContentID(ctx, &contentID)
	if IsNotFound(err) {
		return MediaFile{}, false, nil
	}
	if err != nil {
		return MediaFile{}, false, err
	}
	return mediaFileFrom(m), true, nil
}

// GetMediaByLocalPath looks up a media file by its on-disk path.
func (s *Store) GetMediaByLocalPath(ctx context.Context, localPath string) (MediaFile, bool, error) {
	m, err := s.q.GetMediaByLocalPath(ctx, localPath)
	if IsNotFound(err) {
		return MediaFile{}, false, nil
	}
	if err != nil {
		return MediaFile{}, false, err
	}
	return mediaFileFrom(m), true, nil
}

// SetMediaTelegramFileID caches the Telegram file_id after first upload, so
// later sends can reuse it and skip re-uploading (architecture §2.8, decision 6).
func (s *Store) SetMediaTelegramFileID(ctx context.Context, mediaID uuid.UUID, fileID string) error {
	return s.q.SetMediaTelegramFileID(ctx, db.SetMediaTelegramFileIDParams{ID: mediaID, TelegramFileID: pgText(fileID)})
}

// ListMediaLocalPathsByPrefix returns every media_files.local_path beginning
// with prefix, sorted — used by the cities seeder to learn which city-map
// images have already been uploaded+registered (keyed on the
// "garage://apps-geodrill/citymaps/" prefix).
//
// NOTE: the underlying query is `LIKE prefix || '%'`, so a prefix containing
// SQL LIKE metacharacters (% or _) would misbehave. This is NOT for user
// input — callers pass fixed literal prefixes only.
func (s *Store) ListMediaLocalPathsByPrefix(ctx context.Context, prefix string) ([]string, error) {
	return s.q.ListMediaLocalPathsByPrefix(ctx, pgText(prefix))
}

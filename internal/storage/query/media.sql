-- name: PutMediaFile :one
-- Upsert keyed on local_path (the stable identity of a media asset on disk).
INSERT INTO media_files (content_id, local_path, sha256, telegram_file_id, width, height, bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (local_path) DO UPDATE SET
  content_id = EXCLUDED.content_id,
  sha256 = EXCLUDED.sha256,
  width = EXCLUDED.width,
  height = EXCLUDED.height,
  bytes = EXCLUDED.bytes
RETURNING *;

-- name: GetMediaByContentID :one
SELECT * FROM media_files WHERE content_id = $1;

-- name: GetMediaByLocalPath :one
SELECT * FROM media_files WHERE local_path = $1;

-- name: SetMediaTelegramFileID :exec
-- Caches the Telegram file_id after first upload so later sends can reuse it
-- and skip re-uploading the asset (architecture §2.8, decision 6).
UPDATE media_files SET telegram_file_id = $2 WHERE id = $1;

-- name: ListMediaLocalPathsByPrefix :many
-- Every media_files.local_path beginning with the given prefix — used by the
-- cities seeder to learn which city-map images have been uploaded+registered.
SELECT local_path FROM media_files WHERE local_path LIKE sqlc.arg(prefix) || '%' ORDER BY local_path;

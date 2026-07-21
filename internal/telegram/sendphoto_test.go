package telegram

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// fakeObjectStore records GetObject/PutObject calls so tests can assert whether
// the object store (S3) was read or written, without a live Garage server.
type fakeObjectStore struct {
	getCalls  int
	getBucket string
	getKey    string
	getErr    error
	body      string

	putCalls  int
	putBucket string
	putKey    string
	putBody   []byte
	putErr    error
}

func (f *fakeObjectStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	f.getCalls++
	f.getBucket, f.getKey = bucket, key
	if f.getErr != nil {
		return nil, f.getErr
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func (f *fakeObjectStore) PutObject(_ context.Context, bucket, key string, r io.Reader, _ int64, _ string) error {
	f.putCalls++
	f.putBucket, f.putKey = bucket, key
	if f.putErr != nil {
		return f.putErr
	}
	b, _ := io.ReadAll(r)
	f.putBody = b
	return nil
}

// fakeMediaRegistrar records PutMediaFile so tests can assert an on-demand
// render registered its media_files row and returns the id for write-back.
type fakeMediaRegistrar struct {
	putCalls int
	putPath  string
	returnID uuid.UUID
}

func (f *fakeMediaRegistrar) GetMediaByLocalPath(context.Context, string) (storage.MediaFile, bool, error) {
	return storage.MediaFile{}, false, nil
}
func (f *fakeMediaRegistrar) SetMediaTelegramFileID(context.Context, uuid.UUID, string) error {
	return nil
}
func (f *fakeMediaRegistrar) PutMediaFile(_ context.Context, _ *uuid.UUID, localPath, _ string, _, _, _ *int) (storage.MediaFile, error) {
	f.putCalls++
	f.putPath = localPath
	return storage.MediaFile{ID: f.returnID}, nil
}

// fakeRenderer records RenderPNG so tests can assert the render-on-miss path
// keyed on the right city and used the returned bytes.
type fakeRenderer struct {
	calls int
	key   string
	data  []byte
	ok    bool
	err   error
}

func (f *fakeRenderer) RenderPNG(key string) ([]byte, bool, error) {
	f.calls++
	f.key = key
	return f.data, f.ok, f.err
}

// TestResolvePhotoFile_CachedFileID: a cached telegram_file_id short-circuits —
// the returned File carries the FileID and neither disk, S3, nor the renderer
// is consulted.
func TestResolvePhotoFile_CachedFileID(t *testing.T) {
	objects := &fakeObjectStore{body: "SHOULD-NOT-BE-READ"}
	renderer := &fakeRenderer{data: []byte("NOPE"), ok: true}
	file, rc, effID, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/fr-paris.png", "AgACcachedID", uuid.Nil, objects, nil, renderer, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc != nil {
		t.Fatalf("expected no stream for a cache hit")
	}
	if file.FileID != "AgACcachedID" {
		t.Fatalf("expected cached FileID, got %q", file.FileID)
	}
	if file.FileLocal != "" || file.FileReader != nil {
		t.Fatalf("cache hit must not read disk or S3: %+v", file)
	}
	if objects.getCalls != 0 || renderer.calls != 0 {
		t.Fatalf("cache hit must touch neither object store nor renderer: get=%d render=%d", objects.getCalls, renderer.calls)
	}
	if effID != uuid.Nil {
		t.Fatalf("cache hit must return the passed media id, got %v", effID)
	}
}

// TestResolvePhotoFile_GarageCacheHit: a garage:// ref with no cached file_id
// but a present object streams from S3 — GetObject is called with the parsed
// bucket/key, the renderer is not touched, and the File is reader-backed.
func TestResolvePhotoFile_GarageCacheHit(t *testing.T) {
	objects := &fakeObjectStore{body: "PNGBYTES"}
	renderer := &fakeRenderer{data: []byte("NOPE"), ok: true}
	file, rc, _, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/fr-paris.png", "", uuid.Nil, objects, nil, renderer, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc == nil {
		t.Fatalf("expected a stream to close after send")
	}
	defer rc.Close()
	if objects.getCalls != 1 {
		t.Fatalf("expected exactly one GetObject call, got %d", objects.getCalls)
	}
	if objects.getBucket != "apps-geodrill" || objects.getKey != "citymaps/fr-paris.png" {
		t.Fatalf("GetObject got wrong ref: bucket=%q key=%q", objects.getBucket, objects.getKey)
	}
	if renderer.calls != 0 {
		t.Fatalf("a Garage hit must not render, got %d render calls", renderer.calls)
	}
	if file.FileReader == nil {
		t.Fatalf("expected a reader-backed File, got %+v", file)
	}
}

// TestResolvePhotoFile_RenderOnMiss: a garage:// ref with no cached file_id, an
// object-store MISS, and a renderer that produces bytes uses the rendered bytes
// AND persists them (PutObject + PutMediaFile), returning the new row's id so
// SendPhoto caches the file_id on it.
func TestResolvePhotoFile_RenderOnMiss(t *testing.T) {
	newID := uuid.New()
	objects := &fakeObjectStore{getErr: errors.New("NoSuchKey")}
	media := &fakeMediaRegistrar{returnID: newID}
	renderer := &fakeRenderer{data: []byte("RENDERED-PNG"), ok: true}

	ref := "garage://apps-geodrill/citymaps/fr-paris.png"
	file, rc, effID, err := resolvePhotoFile(context.Background(),
		ref, "", uuid.Nil, objects, media, renderer, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc != nil {
		t.Fatalf("a rendered bytes-reader needs no close, got a non-nil ReadCloser")
	}
	if file.FileReader == nil {
		t.Fatalf("expected a reader-backed File from the render, got %+v", file)
	}
	if renderer.calls != 1 || renderer.key != "fr:paris" {
		t.Fatalf("renderer got calls=%d key=%q, want 1 / fr:paris", renderer.calls, renderer.key)
	}
	if objects.putCalls != 1 || objects.putBucket != "apps-geodrill" || objects.putKey != "citymaps/fr-paris.png" {
		t.Fatalf("PutObject calls=%d bucket=%q key=%q, want 1 / apps-geodrill / citymaps/fr-paris.png",
			objects.putCalls, objects.putBucket, objects.putKey)
	}
	if string(objects.putBody) != "RENDERED-PNG" {
		t.Fatalf("PutObject body = %q, want the rendered bytes", objects.putBody)
	}
	if media.putCalls != 1 || media.putPath != ref {
		t.Fatalf("PutMediaFile calls=%d path=%q, want 1 / %q", media.putCalls, media.putPath, ref)
	}
	if effID != newID {
		t.Fatalf("effective media id = %v, want the newly registered row %v", effID, newID)
	}
}

// TestResolvePhotoFile_RenderNoObjectStore: with NO object store wired, a
// garage:// ref still renders and sends the bytes, but nothing is persisted
// (no PutObject/PutMediaFile) and the effective media id is the passed-in one.
func TestResolvePhotoFile_RenderNoObjectStore(t *testing.T) {
	renderer := &fakeRenderer{data: []byte("RENDERED"), ok: true}
	file, rc, effID, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/fr-paris.png", "", uuid.Nil, nil, nil, renderer, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc != nil {
		t.Fatalf("rendered bytes-reader needs no close")
	}
	if file.FileReader == nil {
		t.Fatalf("expected the rendered bytes to be sent, got %+v", file)
	}
	if renderer.calls != 1 {
		t.Fatalf("expected exactly one render, got %d", renderer.calls)
	}
	if effID != uuid.Nil {
		t.Fatalf("no persistence means the passed media id is returned, got %v", effID)
	}
}

// TestResolvePhotoFile_RenderUnavailable: a garage:// ref with an object miss
// and NO renderer (nil) falls through to a hard error — no bytes to send.
func TestResolvePhotoFile_RenderUnavailable(t *testing.T) {
	objects := &fakeObjectStore{getErr: errors.New("NoSuchKey")}
	_, rc, _, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/fr-paris.png", "", uuid.Nil, objects, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected a hard error when no renderer can produce bytes")
	}
	if rc != nil {
		t.Fatalf("expected no stream on the hard-failure path")
	}
}

// TestResolvePhotoFile_NoStoreNoRenderer: a garage:// ref with neither an
// object store nor a renderer is a hard error (the pre-existing behavior).
func TestResolvePhotoFile_NoStoreNoRenderer(t *testing.T) {
	_, _, _, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/fr-paris.png", "", uuid.Nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected an error when a garage ref has no object store and no renderer")
	}
}

// TestResolvePhotoFile_RenderOkFalseFallsThrough: a renderer that reports
// ok=false (no coordinates) with an object miss falls through to the hard-error
// path rather than sending empty bytes.
func TestResolvePhotoFile_RenderOkFalseFallsThrough(t *testing.T) {
	objects := &fakeObjectStore{getErr: errors.New("NoSuchKey")}
	renderer := &fakeRenderer{ok: false}
	_, _, _, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/fr-paris.png", "", uuid.Nil, objects, nil, renderer, nil)
	if err == nil {
		t.Fatalf("expected a hard error when the renderer has no coords (ok=false)")
	}
	if renderer.calls != 1 {
		t.Fatalf("expected the renderer to be consulted once, got %d", renderer.calls)
	}
}

// TestResolvePhotoFile_DiskPath: a bare disk path (flags) takes the disk branch
// and never touches the object store or renderer — unchanged behavior.
func TestResolvePhotoFile_DiskPath(t *testing.T) {
	objects := &fakeObjectStore{body: "SHOULD-NOT-BE-READ"}
	renderer := &fakeRenderer{data: []byte("NOPE"), ok: true}
	file, rc, _, err := resolvePhotoFile(context.Background(),
		"data/flags/fr.png", "", uuid.Nil, objects, nil, renderer, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc != nil {
		t.Fatalf("disk path needs no stream to close")
	}
	if file.FileLocal != "data/flags/fr.png" {
		t.Fatalf("expected disk-backed File, got %+v", file)
	}
	if file.FileReader != nil || file.FileID != "" {
		t.Fatalf("disk path must not use reader/FileID: %+v", file)
	}
	if objects.getCalls != 0 || objects.putCalls != 0 || renderer.calls != 0 {
		t.Fatalf("disk path must touch neither object store nor renderer")
	}
}

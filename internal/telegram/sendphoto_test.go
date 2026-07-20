package telegram

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeObjectFetcher records GetObject calls so tests can assert whether the
// object store (S3) was touched, without a live Garage server.
type fakeObjectFetcher struct {
	calls  int
	bucket string
	key    string
	body   string
	err    error
}

func (f *fakeObjectFetcher) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	f.calls++
	f.bucket, f.key = bucket, key
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

// TestResolvePhotoFile_CachedFileID: a cached telegram_file_id short-circuits —
// the returned File carries the FileID and neither disk nor S3 is consulted.
func TestResolvePhotoFile_CachedFileID(t *testing.T) {
	fetcher := &fakeObjectFetcher{body: "SHOULD-NOT-BE-READ"}
	file, rc, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/paris.png", "AgACcachedID", fetcher)
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
	if fetcher.calls != 0 {
		t.Fatalf("object store must not be touched on a cache hit, got %d calls", fetcher.calls)
	}
}

// TestResolvePhotoFile_GarageCacheMiss: a garage:// ref with no cached file_id
// and a fetcher streams from S3 — GetObject is called with the parsed
// bucket/key and the File is reader-backed.
func TestResolvePhotoFile_GarageCacheMiss(t *testing.T) {
	fetcher := &fakeObjectFetcher{body: "PNGBYTES"}
	file, rc, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/paris.png", "", fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc == nil {
		t.Fatalf("expected a stream to close after send")
	}
	defer rc.Close()
	if fetcher.calls != 1 {
		t.Fatalf("expected exactly one GetObject call, got %d", fetcher.calls)
	}
	if fetcher.bucket != "apps-geodrill" || fetcher.key != "citymaps/paris.png" {
		t.Fatalf("GetObject got wrong ref: bucket=%q key=%q", fetcher.bucket, fetcher.key)
	}
	if file.FileReader == nil {
		t.Fatalf("expected a reader-backed File, got %+v", file)
	}
	if file.FileLocal != "" || file.FileID != "" {
		t.Fatalf("garage cache miss must not use disk/FileID: %+v", file)
	}
}

// TestResolvePhotoFile_GarageNoFetcher: a garage:// ref with no object store
// wired is a hard error — there are no bytes to send.
func TestResolvePhotoFile_GarageNoFetcher(t *testing.T) {
	_, _, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/paris.png", "", nil)
	if err == nil {
		t.Fatalf("expected an error when a garage ref has no object store")
	}
}

// TestResolvePhotoFile_GarageFetchError: a fetch error propagates (still no
// bytes to send).
func TestResolvePhotoFile_GarageFetchError(t *testing.T) {
	fetcher := &fakeObjectFetcher{err: errors.New("boom")}
	_, rc, err := resolvePhotoFile(context.Background(),
		"garage://apps-geodrill/citymaps/paris.png", "", fetcher)
	if err == nil {
		t.Fatalf("expected the fetch error to propagate")
	}
	if rc != nil {
		t.Fatalf("expected no stream on fetch error")
	}
}

// TestResolvePhotoFile_DiskPath: a bare disk path (flags) takes the disk branch
// and never touches the object store — unchanged behavior.
func TestResolvePhotoFile_DiskPath(t *testing.T) {
	fetcher := &fakeObjectFetcher{body: "SHOULD-NOT-BE-READ"}
	file, rc, err := resolvePhotoFile(context.Background(),
		"data/flags/fr.png", "", fetcher)
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
	if fetcher.calls != 0 {
		t.Fatalf("disk path must not touch the object store, got %d calls", fetcher.calls)
	}
}

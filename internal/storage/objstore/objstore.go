// Package objstore is geodrill's thin wrapper over the Garage S3-compatible
// object store that holds city-map PNGs (vibe/design-cities.md, [[deployment]]).
//
// Design: S3 is a COLD origin, off the hot path. The bot reads an image from
// Garage at most ONCE per image — on the first Telegram send, to seed the
// telegram_file_id cache in media_files; every later send reuses the cached
// file_id and transfers no bytes (internal/telegram's SendPhoto). So this
// package is deliberately small: put/get/stat plus a ref parser. The Store
// interface is what higher layers depend on and fake in tests; the minio
// implementation is never exercised against a live server in the gate.
package objstore

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// garageScheme is the URI scheme prefix that marks a media_files.local_path as
// a Garage object reference ("garage://<bucket>/<key...>") rather than a bare
// on-disk path (flags keep "data/flags/xx.png"). The two coexist by scheme so
// SendPhoto has one send path, not a fork (vibe/design-cities.md).
const garageScheme = "garage://"

// Store is the narrow object-store surface geodrill needs. Higher layers
// depend on this interface (not the concrete minio client) so they can be
// faked in unit tests without a live S3.
type Store interface {
	// PutObject uploads size bytes read from r under bucket/key with the given
	// content type.
	PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error
	// GetObject returns a streaming ReadCloser for bucket/key. A missing
	// object surfaces as an error here (not lazily on first Read); the caller
	// must Close the returned reader.
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	// StatObject reports whether bucket/key exists and, if so, its size. A
	// missing object is (false, 0, nil) — not an error.
	StatObject(ctx context.Context, bucket, key string) (exists bool, size int64, err error)
}

// minioStore is the minio-go-backed Store implementation.
type minioStore struct {
	client *minio.Client
}

// New builds a minioStore for a Garage endpoint. endpoint may include a scheme
// (http:// or https://); the scheme both selects TLS and is stripped before
// being handed to minio.New (which wants a bare host:port). Garage requires
// path-style addressing, so BucketLookup is forced to path rather than relying
// on minio's non-AWS auto-detection. region is passed through to the client.
func New(endpoint, region, accessKey, secretKey string) (*minioStore, error) {
	secure := false
	host := endpoint
	switch {
	case strings.HasPrefix(host, "https://"):
		secure = true
		host = strings.TrimPrefix(host, "https://")
	case strings.HasPrefix(host, "http://"):
		host = strings.TrimPrefix(host, "http://")
	}
	host = strings.TrimRight(host, "/")

	client, err := minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:       secure,
		Region:       region,
		BucketLookup: minio.BucketLookupPath, // Garage is path-style only
	})
	if err != nil {
		return nil, fmt.Errorf("objstore: new minio client: %w", err)
	}
	return &minioStore{client: client}, nil
}

func (s *minioStore) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("objstore: put %s/%s: %w", bucket, key, err)
	}
	return nil
}

func (s *minioStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("objstore: get %s/%s: %w", bucket, key, err)
	}
	// minio's GetObject is lazy — it returns no error until the first Read/Stat
	// even for a missing object. Stat here so a 404 surfaces eagerly as a clear
	// error rather than as a confusing partial read downstream.
	if _, serr := obj.Stat(); serr != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("objstore: get %s/%s: %w", bucket, key, serr)
	}
	return obj, nil
}

func (s *minioStore) StatObject(ctx context.Context, bucket, key string) (bool, int64, error) {
	info, err := s.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("objstore: stat %s/%s: %w", bucket, key, err)
	}
	return true, info.Size, nil
}

// isNotFound reports whether err is minio's object-not-found response
// (NoSuchKey / HTTP 404), which StatObject maps to a clean "does not exist"
// rather than an error.
func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}

// ParseGarageRef parses a "garage://<bucket>/<key...>" reference into its
// bucket and key. ok is false for anything without the garage:// prefix (e.g.
// a bare disk path like "data/flags/xx.png") or a malformed ref missing the
// bucket or key. The key may itself contain slashes ("citymaps/foo.png").
func ParseGarageRef(s string) (bucket, key string, ok bool) {
	if !strings.HasPrefix(s, garageScheme) {
		return "", "", false
	}
	rest := strings.TrimPrefix(s, garageScheme)
	i := strings.IndexByte(rest, '/')
	if i <= 0 { // no slash, or leading slash (empty bucket)
		return "", "", false
	}
	bucket = rest[:i]
	key = rest[i+1:]
	if key == "" {
		return "", "", false
	}
	return bucket, key, true
}

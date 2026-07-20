// Command citymapsync uploads local city-map PNGs to the Garage S3 object
// store and registers each as a media_files row keyed on its
// "garage://<bucket>/citymaps/<file>" reference — the S3 counterpart to
// cmd/flagassets (which only registers a bare on-disk path). The cities topic
// resolves an item's map image by this garage:// ref; the bot streams the
// bytes from Garage exactly once, on the first Telegram send, to seed the
// telegram_file_id cache (internal/telegram SendPhoto, vibe/design-cities.md).
//
// It mirrors cmd/flagassets's structure (config.Load(false), MigrateUp,
// storage.New, walk *.png, per-file sha256 + image.DecodeConfig dims) but adds
// the upload step. Idempotent: the media upsert is keyed on the ref, and
// unless -force an object of the same size already present in the bucket is
// left in place (only the media_files row is (re)ensured).
//
// Requires Garage credentials (config.GarageConfigured) — it cannot run
// without them. Never commits anything under data/ (that directory is
// gitignored, populated out of band).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder image.DecodeConfig needs
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/supercakecrumb/geodrill/internal/config"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/objstore"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "citymapsync: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	mediaRoot := flag.String("media-root", "data/citymaps", "directory of city-map PNGs to upload (every *.png directly under it)")
	force := flag.Bool("force", false, "re-upload every object even when one of the same size already exists in the bucket")
	flag.Parse()

	cfg, err := config.Load(false)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := config.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	if !cfg.GarageConfigured() {
		return fmt.Errorf("garage object store is not configured: set GARAGE_S3_ENDPOINT, GARAGE_ACCESS_KEY_ID and GARAGE_SECRET_ACCESS_KEY (this tool cannot run without them)")
	}

	ctx := context.Background()

	logger.Info("applying migrations")
	if err := storage.MigrateUp(storage.MigrateURL(cfg.DatabaseURL)); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	store, err := storage.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	objStore, err := objstore.New(cfg.GarageEndpoint, cfg.GarageRegion, cfg.GarageAccessKey, cfg.GarageSecretKey)
	if err != nil {
		return fmt.Errorf("build object store: %w", err)
	}
	bucket := cfg.GarageBucket

	files, err := listPNGs(*mediaRoot)
	if err != nil {
		return fmt.Errorf("list %s: %w", *mediaRoot, err)
	}
	if len(files) == 0 {
		logger.Warn("no PNG files found under media root", "media_root", *mediaRoot)
	}

	var uploaded, skipped, failed int
	for _, path := range files {
		didUpload, err := syncOne(ctx, store, objStore, bucket, path, *force)
		if err != nil {
			logger.Error("sync city map failed", "path", path, "error", err)
			failed++
			continue
		}
		if didUpload {
			uploaded++
		} else {
			skipped++
		}
	}

	logger.Info("citymapsync: done",
		"media_root", *mediaRoot, "bucket", bucket, "total", len(files),
		"uploaded", uploaded, "skipped", skipped, "failed", failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d city maps failed to sync; see log above", failed, len(files))
	}
	return nil
}

// listPNGs returns every *.png file directly under root, sorted for
// deterministic log output (each upload/upsert is independent, keyed on its
// object key / media ref).
func listPNGs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".png" {
			continue
		}
		out = append(out, filepath.Join(root, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// syncOne uploads path to Garage (unless an object of the same size already
// exists and -force is off) and upserts its media_files row keyed on the
// "garage://<bucket>/citymaps/<file>" reference. It returns whether it actually
// uploaded bytes (false = the object was already present and reused). The
// media row is (re)ensured either way, so a run always leaves the DB consistent
// with the bucket.
func syncOne(ctx context.Context, store *storage.Store, objStore objstore.Store, bucket, path string, force bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	imgCfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("decode PNG dimensions: %w", err)
	}
	width, height, nBytes := imgCfg.Width, imgCfg.Height, len(data)

	key := "citymaps/" + filepath.Base(path)
	ref := "garage://" + bucket + "/" + key

	uploaded := false
	if !force {
		exists, size, err := objStore.StatObject(ctx, bucket, key)
		if err != nil {
			return false, fmt.Errorf("stat object: %w", err)
		}
		if !exists || size != int64(nBytes) {
			if err := objStore.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(nBytes), "image/png"); err != nil {
				return false, fmt.Errorf("put object: %w", err)
			}
			uploaded = true
		}
	} else {
		if err := objStore.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(nBytes), "image/png"); err != nil {
			return false, fmt.Errorf("put object: %w", err)
		}
		uploaded = true
	}

	// content_id is left nil (like flags): the cities topic resolves an item's
	// image by its garage:// ref directly, not via a content_items link.
	if _, err := store.PutMediaFile(ctx, nil, ref, hexSum, &width, &height, &nBytes); err != nil {
		return false, fmt.Errorf("put media file: %w", err)
	}
	return uploaded, nil
}

// Command citymapsync is an OPTIONAL pre-warm tool: it renders every city map
// in-process and uploads each to the Garage S3 object store, registering a
// media_files row keyed on its "garage://<bucket>/citymaps/<file>" reference.
//
// It is no longer required. The bot renders city maps ON DEMAND at first send
// (internal/telegram SendPhoto → internal/citymap.Renderer) and, when Garage is
// configured, persists them there — so maps appear with no offline batch. This
// tool exists only to PRE-WARM the Garage cache ahead of time, avoiding the
// small first-send render+upload latency for the whole set at once.
//
// It reuses the SAME building blocks the bot's on-demand path uses — the
// citymap renderer (RenderPNG), objstore.PutObject, and store.PutMediaFile — so
// there is one render/upload/register implementation, not two. Requires Garage
// credentials (config.GarageConfigured) and the Natural Earth basemap
// (NATURAL_EARTH_PATH). Never commits anything under data/.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/supercakecrumb/geodrill/internal/citymap"
	"github.com/supercakecrumb/geodrill/internal/config"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/objstore"
	"github.com/supercakecrumb/geodrill/internal/topics/cities"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "citymapsync: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	nePath := flag.String("ne", "", "path to the Natural Earth countries GeoJSON (default: NATURAL_EARTH_PATH, else data/naturalearth/ne_10m_admin_0_countries.geojson)")
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

	neFile := cfg.NaturalEarthPath
	if *nePath != "" {
		neFile = *nePath
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

	renderer, err := citymap.NewRenderer(neFile, cities.CitiesSeedPath())
	if err != nil {
		return fmt.Errorf("build city-map renderer: %w", err)
	}

	keys := renderer.Keys()
	var uploaded, skipped, skippedNoCoord, failed int
	for _, key := range keys {
		data, ok, err := renderer.RenderPNG(key)
		if err != nil {
			logger.Error("render city map failed", "key", key, "error", err)
			failed++
			continue
		}
		if !ok {
			// No coordinates — the bot serves these as text, nothing to pre-warm.
			skippedNoCoord++
			continue
		}
		didUpload, err := syncRendered(ctx, store, objStore, bucket, key, data, *force)
		if err != nil {
			logger.Error("sync city map failed", "key", key, "error", err)
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
		"bucket", bucket, "total", len(keys),
		"uploaded", uploaded, "skipped", skipped, "skipped_no_coords", skippedNoCoord, "failed", failed)
	if failed > 0 {
		return fmt.Errorf("%d city map(s) failed to sync; see log above", failed)
	}
	return nil
}

// syncRendered uploads the rendered bytes for one city to Garage (unless an
// object of the same size already exists and -force is off) and upserts its
// media_files row keyed on "garage://<bucket>/citymaps/<file>" — the exact
// PutObject + PutMediaFile pair the bot's on-demand render-on-miss path
// (internal/telegram resolvePhotoFile) runs, so the pre-warmed and on-demand
// results are identical. Returns whether it actually uploaded bytes.
func syncRendered(ctx context.Context, store *storage.Store, objStore objstore.Store, bucket, key string, data []byte, force bool) (bool, error) {
	file := citymap.ImageFileName(key)
	objKey := "citymaps/" + file
	ref := "garage://" + bucket + "/" + objKey

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	width, height, nBytes := citymap.ImageWidth, citymap.ImageHeight, len(data)

	uploaded := false
	if !force {
		exists, size, err := objStore.StatObject(ctx, bucket, objKey)
		if err != nil {
			return false, fmt.Errorf("stat object: %w", err)
		}
		if !exists || size != int64(nBytes) {
			if err := objStore.PutObject(ctx, bucket, objKey, bytes.NewReader(data), int64(nBytes), "image/png"); err != nil {
				return false, fmt.Errorf("put object: %w", err)
			}
			uploaded = true
		}
	} else {
		if err := objStore.PutObject(ctx, bucket, objKey, bytes.NewReader(data), int64(nBytes), "image/png"); err != nil {
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

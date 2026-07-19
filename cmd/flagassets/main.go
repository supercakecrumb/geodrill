// Command flagassets ingests the local flag PNG cache (data/flags/ by
// default — gitignored, populated by scripts/fetch-flags.sh) into
// media_files: one row per PNG on disk, with sha256 + decoded width/height,
// upserted via internal/storage/media.go's PutMediaFile (keyed on
// local_path — idempotent, so re-running after re-fetching an asset just
// updates the existing row rather than duplicating it).
//
// content_id is always left nil: the flags topic (internal/topics/flags)
// resolves an item's image by local_path directly (its items.payload caches
// the bare filename, joined against the media root at generation time), not
// via a content_items link — see vibe/design-flags-quiz.md §5.
//
// Isolated from cmd/ingest per architecture §8's conflict-avoidance rule
// (a new, narrowly-scoped binary rather than a mode bolted onto an existing
// one two workers might be touching at once).
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
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "flagassets: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	mediaRoot := flag.String("media-root", "data/flags", "directory of flag PNGs to ingest (every *.png directly under it)")
	flag.Parse()

	cfg, err := config.Load(false)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := config.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

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

	files, err := listPNGs(*mediaRoot)
	if err != nil {
		return fmt.Errorf("list %s: %w", *mediaRoot, err)
	}
	if len(files) == 0 {
		logger.Warn("no PNG files found under media root", "media_root", *mediaRoot)
	}

	var upserted, failed int
	for _, path := range files {
		if err := ingestOne(ctx, store, path); err != nil {
			logger.Error("ingest flag asset failed", "path", path, "error", err)
			failed++
			continue
		}
		upserted++
	}

	logger.Info("flagassets: done", "media_root", *mediaRoot, "total", len(files), "upserted", upserted, "failed", failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d flag assets failed to ingest; see log above", failed, len(files))
	}
	return nil
}

// listPNGs returns every *.png file directly under root, sorted for
// deterministic, readable log output (PutMediaFile itself is
// order-independent: it upserts one row per call, keyed on local_path).
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

// ingestOne computes path's sha256 + decoded PNG dimensions and upserts its
// media_files row (see this command's package doc for the local_path
// keying / nil content_id rationale).
func ingestOne(ctx context.Context, store *storage.Store, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode PNG dimensions: %w", err)
	}
	width, height, nBytes := cfg.Width, cfg.Height, len(data)

	if _, err := store.PutMediaFile(ctx, nil, path, hexSum, &width, &height, &nBytes); err != nil {
		return fmt.Errorf("put media file: %w", err)
	}
	return nil
}

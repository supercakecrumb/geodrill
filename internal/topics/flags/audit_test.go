package flags

// TestAuditMediaAssets is an opt-in asset-consistency audit (design §8),
// not a regular unit test: it checks every seeds/flags.yaml image
// reference against the real on-disk flag PNG cache. Gated on
// FLAGS_AUDIT_MEDIA_ROOT (skip if unset — the media root is gitignored, so
// CI without it configured shouldn't fail) plus the standard
// GEODRILL_TEST_DATABASE_URL "test"-name fuse, since this test seeds a
// fresh schema and self-ingests (mirrors cmd/flagassets) rather than
// depending on some other test in this package having already run first.
//
// Design §8's remaining checks — no country double-claimed across shapes
// (single vs confusable group), every confusable-group ISO resolves, and
// len(images) == len(countries) per group — need no filesystem access and
// are already asserted unconditionally by TestLoadFlagsRealSeed
// (seed_test.go) and TestSeedAndConsistency (integration_test.go); this
// test focuses on the one check that needs real files: every referenced
// image actually exists on disk (catches a typo'd/renamed/deleted
// filename) and the storage layer's PutMediaFile/GetMediaByLocalPath
// round-trip is consistent for it.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name
// contains "test" — copied from this package's own integration_test.go
// (duplicated rather than shared across the internal/external test
// packages, matching every other topic package's convention).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

func auditTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run the flags media audit")
	}
	if name := auditDatabaseName(dsn); !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run destructive integration tests against database %q: "+
			"GEODRILL_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\" "+
			"(e.g. geodrill_test), never the live database", name)
	}
	return dsn
}

func auditDatabaseName(dsn string) string {
	s := dsn
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return ""
	}
	return strings.Trim(s[i+1:], "/")
}

func auditFreshSchema(t *testing.T, dsn string) {
	t.Helper()
	url := storage.MigrateURL(dsn)
	if err := storage.MigrateUp(url); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := storage.MigrateDown(url); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := storage.MigrateUp(url); err != nil {
		t.Fatalf("migrate up (again): %v", err)
	}
}

func TestAuditMediaAssets(t *testing.T) {
	mediaRoot := os.Getenv("FLAGS_AUDIT_MEDIA_ROOT")
	if mediaRoot == "" {
		t.Skip("FLAGS_AUDIT_MEDIA_ROOT not set; skipping flag-asset audit")
	}
	dsn := auditTestDSN(t)
	auditFreshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	if err := Seed(ctx, store); err != nil {
		t.Fatalf("seed flags: %v", err)
	}

	sf, err := loadFlagsFile(flagsSeedPath())
	if err != nil {
		t.Fatalf("loadFlagsFile: %v", err)
	}

	var missing, roundTripFailed []string
	checkOne := func(label, image string) {
		if image == "" {
			return // no asset ingested yet for this item (design §6 fallback) — nothing to audit
		}
		path := filepath.Join(mediaRoot, image)
		data, err := os.ReadFile(path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s -> %s: %v", label, path, err))
			return
		}
		if len(data) == 0 {
			missing = append(missing, fmt.Sprintf("%s -> %s: file is empty", label, path))
			return
		}
		sum := sha256.Sum256(data)
		hexSum := hex.EncodeToString(sum[:])

		mf, err := store.PutMediaFile(ctx, nil, path, hexSum, nil, nil, nil)
		if err != nil {
			t.Fatalf("PutMediaFile %s: %v", path, err)
		}
		got, found, err := store.GetMediaByLocalPath(ctx, path)
		if err != nil {
			t.Fatalf("GetMediaByLocalPath %s: %v", path, err)
		}
		if !found || got.ID != mf.ID || got.SHA256 != hexSum {
			roundTripFailed = append(roundTripFailed, fmt.Sprintf("%s -> %s", label, path))
		}
	}

	for _, e := range sf.Flags {
		checkOne(e.Country, e.Image)
	}
	for _, g := range sf.ConfusableGroups {
		if len(g.Images) != len(g.Countries) {
			missing = append(missing, fmt.Sprintf("group %s: %d images for %d countries", g.Group, len(g.Images), len(g.Countries)))
			continue
		}
		for i, img := range g.Images {
			label := fmt.Sprintf("%s[%d]=%s", g.Group, i, g.Countries[i])
			checkOne(label, img)
		}
	}

	if len(missing) > 0 {
		t.Errorf("missing/empty flag assets under %s:\n%s", mediaRoot, strings.Join(missing, "\n"))
	}
	if len(roundTripFailed) > 0 {
		t.Errorf("media_files put/get round-trip inconsistent for:\n%s", strings.Join(roundTripFailed, "\n"))
	}
}

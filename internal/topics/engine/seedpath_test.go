package engine

import (
	"os"
	"testing"
)

// TestSeedPath_ResolvesRepoSeeds asserts that, when run via `go test` (cwd =
// this package's own directory), SeedPath resolves seeds/countries.yaml to a
// path that actually exists on disk — proving the runtime.Caller branch
// finds the real repo seeds/ directory rather than silently falling through
// to the container-only "seeds/<name>" fallback.
func TestSeedPath_ResolvesRepoSeeds(t *testing.T) {
	path := SeedPath("countries.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SeedPath(%q) = %q, which does not exist: %v", "countries.yaml", path, err)
	}
}

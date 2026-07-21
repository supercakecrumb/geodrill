package engine

import (
	"os"
	"path/filepath"
	"runtime"
)

// SeedPath resolves the on-disk path to a committed seed file under seeds/,
// working in every environment geodrill runs in:
//
//   - `go test` (cwd = the calling package's own directory) and repo-root
//     runs (cmd/bot, cmd/ingest from the repo): the source-relative path
//     derived from runtime.Caller points at the repo's seeds/ directory and
//     exists on disk, so it's used.
//   - the distroless container: the binary is built with -trimpath, so
//     runtime.Caller reports a module-relative path
//     (github.com/supercakecrumb/geodrill/...) that does NOT exist on disk;
//     the Dockerfile instead copies seeds/ to /app/seeds and runs with
//     WORKDIR /app. We detect the missing source-relative path and fall back
//     to "seeds/<name>" resolved against the working directory (→ /app/seeds).
//
// Centralizing this here (rather than a runtime.Caller helper copied into
// every topic package) keeps the container fallback in one place — the copies
// silently worked locally but broke bot/ingest startup in Docker, since the
// bot loads seeds/*.yaml lookup tables at startup, not just at ingest time.
func SeedPath(name string) string {
	if _, file, _, ok := runtime.Caller(0); ok {
		// engine lives at internal/topics/engine → ../../../seeds = repo-root seeds/.
		p := filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Container / any run whose working directory has seeds/ beside it.
	return filepath.Join("seeds", name)
}

package content

import (
	"compress/bzip2"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// tatoebaURL builds the per-language export URL (architecture contract §6).
func tatoebaURL(lang string) string {
	return fmt.Sprintf("https://downloads.tatoeba.org/exports/per_language/%s/%s_sentences.tsv.bz2", lang, lang)
}

// httpClientTimeout bounds the download of a single language dump.
const httpClientTimeout = 5 * time.Minute

// DownloadDump ensures the compressed Tatoeba dump for lang is present in
// dataDir, downloading it if necessary, and returns its path.
//
// If skipDownload is true, the cached file must already exist (and be
// non-empty); no network request is made and a missing/empty cache is an
// error. Otherwise, an existing non-empty cache file is reused as-is; a
// missing or empty one triggers a fresh download.
//
// Downloads are streamed to a temp file in dataDir and renamed into place
// once complete, so a crash mid-download never leaves a corrupt cache file
// that looks valid.
func DownloadDump(ctx context.Context, dataDir, lang string, skipDownload bool) (string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}

	path := filepath.Join(dataDir, lang+"_sentences.tsv.bz2")

	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return path, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat cache file: %w", err)
	}

	if skipDownload {
		return "", fmt.Errorf("no cached dump for %q at %s and -skip-download is set", lang, path)
	}

	if err := downloadTo(ctx, tatoebaURL(lang), path); err != nil {
		return "", fmt.Errorf("download %s: %w", lang, err)
	}
	return path, nil
}

// downloadTo streams url's body to a temp file in dest's directory, then
// renames it to dest on success.
func downloadTo(ctx context.Context, url, dest string) error {
	client := &http.Client{Timeout: httpClientTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath) // no-op once renamed
	}()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// OpenDecompressed opens the cached .bz2 dump at path and returns a
// decompressing reader over its contents, along with the underlying *os.File
// (which the caller must Close once done reading).
func OpenDecompressed(path string) (io.Reader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	return bzip2.NewReader(f), f, nil
}

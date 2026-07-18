package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// siteBaseURL is plonkit.net's canonical origin (matches sitemap.xml's
// <loc> entries and the "canonical" link tag on every guide page).
const siteBaseURL = "https://www.plonkit.net"

// userAgent identifies this tool honestly (product name, contact, and the
// authorization context) rather than spoofing a browser or a search-engine
// bot — see robots.go and vibe/plonkit-topics.md for why that distinction
// matters against this specific site's robots.txt.
const userAgent = "geodrill-plonkit-scraper/0.1 (+github.com/supercakecrumb/geodrill; authorized by site owner)"

// fetcher performs polite, cached HTTP GETs against plonkit.net: at most one
// live network request per delay interval, a simple on-disk cache so re-runs
// never re-fetch a URL already seen, and a robots.txt courtesy check (see
// robots.go) before every live request.
type fetcher struct {
	client   *http.Client
	cacheDir string
	delay    time.Duration
	robots   *robotsRules // nil until loadRobots is called

	last time.Time // zero until the first live request
}

// newFetcher builds a fetcher writing its cache under cacheDir.
func newFetcher(cacheDir string, delay time.Duration) *fetcher {
	return &fetcher{
		client:   &http.Client{Timeout: 30 * time.Second},
		cacheDir: cacheDir,
		delay:    delay,
	}
}

// get returns the body of rawURL. path is rawURL's path component (e.g.
// "/netherlands"), used only for the robots.txt courtesy check;
// skipRobotsCheck is true only when fetching robots.txt itself (nothing to
// check it against yet) or before f.robots has been loaded. cached reports
// whether the body came from the on-disk cache (no network request made).
func (f *fetcher) get(ctx context.Context, rawURL, path string, skipRobotsCheck bool) (body []byte, cached bool, err error) {
	key := cacheKey(rawURL)
	cachePath := filepath.Join(f.cacheDir, key)

	if b, err := os.ReadFile(cachePath); err == nil {
		return b, true, nil
	} else if !os.IsNotExist(err) {
		return nil, false, fmt.Errorf("read cache %s: %w", cachePath, err)
	}

	if !skipRobotsCheck && f.robots != nil && !f.robots.allowed(path) {
		return nil, false, fmt.Errorf("robots.txt disallows %s for our user-agent group (courtesy check — see robots.go)", path)
	}

	f.throttle()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("build request for %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read body of %s: %w", rawURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("GET %s: unexpected status %s", rawURL, resp.Status)
	}

	if err := os.MkdirAll(f.cacheDir, 0o755); err != nil {
		return nil, false, fmt.Errorf("mkdir cache dir %s: %w", f.cacheDir, err)
	}
	if err := os.WriteFile(cachePath, body, 0o644); err != nil {
		return nil, false, fmt.Errorf("write cache %s: %w", cachePath, err)
	}

	return body, false, nil
}

// throttle blocks, if necessary, so that consecutive LIVE requests (cache
// hits never call this) are spaced at least f.delay apart — the "≤1
// request/sec" politeness budget the task brief requires.
func (f *fetcher) throttle() {
	if f.delay <= 0 {
		return
	}
	if f.last.IsZero() {
		f.last = time.Now()
		return
	}
	if elapsed := time.Since(f.last); elapsed < f.delay {
		time.Sleep(f.delay - elapsed)
	}
	f.last = time.Now()
}

// cacheKey turns a URL into a flat, readable on-disk filename, e.g.
// "https://www.plonkit.net/netherlands" -> "www.plonkit.net_netherlands".
func cacheKey(rawURL string) string {
	key := strings.TrimPrefix(rawURL, "https://")
	key = strings.TrimPrefix(key, "http://")
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, key)
}

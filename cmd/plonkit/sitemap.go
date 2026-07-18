package main

import (
	"fmt"
	"regexp"
	"strings"
)

// locRe extracts the URL out of a sitemap.xml "<loc>...</loc>" entry.
var locRe = regexp.MustCompile(`<loc>\s*([^<\s]+)\s*</loc>`)

// guidePagesMarker is the literal XML comment plonkit.net's sitemap.xml uses
// to mark where its per-country/-region guide URLs begin (verified
// 2026-07-18: everything before it is homepage/records/rules/tools/community
// pages; every <loc> after it, up to EOF, is a guide page — 138 entries at
// the time of writing, two of which, "beginners-guide" and
// "spillover-countries", are guide-shaped meta pages rather than countries;
// see vibe/plonkit-topics.md).
const guidePagesMarker = "<!-- Guide Pages -->"

// discoverGuideIndex parses sitemap.xml into the set of guide-page URL
// slugs. This is the "discover the guide index from the site's own
// structure" step the task brief asks for: it finds slugs generically
// rather than hardcoding a fixed country list, so it keeps working if
// plonkit adds, renames, or removes guides — resolveCountry cross-checks
// every requested country's slug against this freshly discovered set rather
// than trusting the embedded ISO table blindly.
func discoverGuideIndex(sitemapBody []byte) ([]string, error) {
	text := string(sitemapBody)
	idx := strings.Index(text, guidePagesMarker)
	if idx < 0 {
		return nil, fmt.Errorf("sitemap.xml: %q marker not found — site structure may have changed", guidePagesMarker)
	}

	matches := locRe.FindAllStringSubmatch(text[idx:], -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("sitemap.xml: no <loc> entries found after %q", guidePagesMarker)
	}

	slugs := make([]string, 0, len(matches))
	for _, m := range matches {
		slugs = append(slugs, slugFromURL(m[1]))
	}
	return slugs, nil
}

// slugFromURL returns the last path segment of a URL, e.g.
// "https://www.plonkit.net/netherlands" -> "netherlands".
func slugFromURL(u string) string {
	u = strings.TrimSuffix(u, "/")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return u
}

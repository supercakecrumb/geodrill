// Command plonkit is a prototype scraper for plonkit.net, a GeoGuessr
// country-guide site: it learns the site's structure, extracts structured
// per-country tip data, and derives a candidate meta-topic taxonomy for
// geodrill v2's tier-5 topics (architecture §4, task W5.5). Aurora has the
// site developer's explicit permission to scrape and reuse the content for
// this purpose; see vibe/plonkit-topics.md for the robots.txt findings this
// tool nonetheless honors as a courtesy (robots.go).
//
// Fully isolated from the rest of geodrill: no shared internal packages, no
// database — output is plain YAML under -out, which stays gitignored
// (data/) and is never committed. Manual runs for now; cron-able later.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// scrapeResult summarizes one requested country's outcome for the final
// stdout report (printSummary).
type scrapeResult struct {
	iso  string
	tips int
	err  error
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "plonkit: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "data/plonkit", "output directory for scraped YAML files (must stay gitignored — data/ already is)")
	countriesFlag := flag.String("countries", "nl,de,fr", "comma-separated ISO 3166-1 alpha-2 country codes to scrape")
	delay := flag.Duration("delay", time.Second, "minimum delay between live HTTP requests (politeness budget)")
	limit := flag.Int("limit", 0, "cap the number of countries actually scraped this run (0 = no cap beyond -countries)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	f := newFetcher(filepath.Join(*out, ".cache"), *delay)

	logger.Info("fetching robots.txt")
	robotsBody, robotsCached, err := f.get(ctx, siteBaseURL+"/robots.txt", "/robots.txt", true)
	if err != nil {
		return fmt.Errorf("fetch robots.txt: %w", err)
	}
	f.robots = parseRobots(robotsBody, userAgent)
	logger.Info("robots.txt parsed", "cached", robotsCached, "applicable_rules", len(f.robots.rules))

	logger.Info("discovering guide index from sitemap.xml")
	sitemapBody, sitemapCached, err := f.get(ctx, siteBaseURL+"/sitemap.xml", "/sitemap.xml", false)
	if err != nil {
		return fmt.Errorf("fetch sitemap.xml: %w", err)
	}
	slugs, err := discoverGuideIndex(sitemapBody)
	if err != nil {
		return fmt.Errorf("discover guide index: %w", err)
	}
	guideIndex := make(map[string]struct{}, len(slugs))
	for _, s := range slugs {
		guideIndex[s] = struct{}{}
	}
	logger.Info("guide index discovered", "cached", sitemapCached, "slugs", len(slugs))

	codes := splitCountries(*countriesFlag)
	if len(codes) == 0 {
		return fmt.Errorf("no countries requested (-countries is empty)")
	}
	if *limit > 0 && *limit < len(codes) {
		logger.Info("applying -limit", "requested", len(codes), "limit", *limit)
		codes = codes[:*limit]
	}

	counter := newTagCounter()
	var sampleISO []string
	results := make([]scrapeResult, 0, len(codes))

	for _, iso := range codes {
		slug, err := resolveCountry(iso, guideIndex)
		if err != nil {
			results = append(results, scrapeResult{iso: iso, err: err})
			logger.Error("resolve failed", "iso", iso, "error", err)
			continue
		}

		pageURL := siteBaseURL + "/" + slug
		body, cached, err := f.get(ctx, pageURL, "/"+slug, false)
		if err != nil {
			results = append(results, scrapeResult{iso: iso, err: err})
			logger.Error("fetch failed", "iso", iso, "slug", slug, "error", err)
			continue
		}

		guide, err := parseGuidePage(body)
		if err != nil {
			results = append(results, scrapeResult{iso: iso, err: err})
			logger.Error("parse failed", "iso", iso, "slug", slug, "error", err)
			continue
		}

		doc := buildCountryDoc(iso, slug, guide, time.Now().UTC())
		counter.add(doc)
		sampleISO = append(sampleISO, iso)

		docPath := filepath.Join(*out, strings.ToLower(iso)+".yaml")
		if err := writeYAML(docPath, doc); err != nil {
			results = append(results, scrapeResult{iso: iso, err: err})
			logger.Error("write failed", "iso", iso, "error", err)
			continue
		}

		tipCount := 0
		for _, sec := range doc.Sections {
			tipCount += len(sec.Tips)
		}
		results = append(results, scrapeResult{iso: iso, tips: tipCount})
		logger.Info("scraped country", "iso", iso, "slug", slug, "cached", cached, "sections", len(doc.Sections), "tips", tipCount)
	}

	if len(sampleISO) > 0 {
		taxPath := filepath.Join(*out, "taxonomy.yaml")
		if err := writeYAML(taxPath, counter.build(sampleISO)); err != nil {
			return fmt.Errorf("write taxonomy: %w", err)
		}
		logger.Info("taxonomy written", "path", taxPath)
	}

	printSummary(results)

	failed := 0
	for _, r := range results {
		if r.err != nil {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d/%d countries failed — see log above", failed, len(results))
	}
	return nil
}

// splitCountries parses a comma-separated ISO2 list, upper-casing and
// de-duplicating while preserving first-seen order.
func splitCountries(s string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func printSummary(results []scrapeResult) {
	fmt.Println()
	fmt.Println("iso\ttips\tstatus")
	for _, r := range results {
		status := "ok"
		if r.err != nil {
			status = "FAILED: " + r.err.Error()
		}
		fmt.Printf("%s\t%d\t%s\n", r.iso, r.tips, status)
	}
}

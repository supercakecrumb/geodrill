// Command cityfacts is a resumable, rate-limited scraper that fetches a
// one-paragraph landmark/fact blurb for the higher-tier cities in
// seeds/cities.yaml and writes seeds/city_facts.yaml.
//
// Cities are matched to a Wikipedia article primarily via Wikidata (CC0),
// batched SPARQL against https://query.wikidata.org/sparql, joining on the
// GeoNames ID already recorded in seeds/cities.yaml (property P1566).
// Cities with no geoname_id, or no Wikidata hit, fall back to Wikipedia's
// GeoSearch API by coordinates. The blurb itself is the Wikipedia REST
// page-summary "extract", which is CC BY-SA 4.0 and requires attribution —
// carried per-entry via source_url in the output file.
//
// Usage:
//
//	go run ./cmd/cityfacts [-cities seeds/cities.yaml] [-out seeds/city_facts.yaml]
//	    [-tier-max 2] [-limit 0] [-rps 2] [-force]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

const userAgent = "geodrill-cityfacts/1.0 (+https://github.com/supercakecrumb/geodrill; contact: supercakecrumb@gmail.com)"

const factsFileHeader = `# seeds/city_facts.yaml — one-paragraph landmark/fact blurbs for the
# higher-tier cities in seeds/cities.yaml, later folded into the "city
# discovered" intro card for tier 0-2 cities.
#
# Blurbs are Wikipedia page-summary extracts (the lead paragraph, via the
# REST API /page/summary/<title> endpoint) and are licensed
# CC BY-SA 4.0 (https://creativecommons.org/licenses/by-sa/4.0/) — reusing
# this text REQUIRES attribution, carried per-entry via source_url (a link
# to the exact Wikipedia article each blurb came from).
#
# Cities are matched to a Wikipedia article primarily via Wikidata (CC0,
# https://www.wikidata.org/), batched SPARQL joining on the GeoNames ID
# already recorded in seeds/cities.yaml (property P1566). Cities with no
# geoname_id, or no Wikidata hit, fall back to Wikipedia's GeoSearch API by
# coordinates.
#
# Regenerate (adds new cities; -force re-scrapes existing keys) with:
#   go run ./cmd/cityfacts
#
# Schema:
#   key: matches the city's key in seeds/cities.yaml
#   blurb: trimmed Wikipedia summary extract (<=2 sentences, <=350 chars)
#   source_title: the matched Wikipedia article title
#   source_url: the matched Wikipedia article URL (attribution link)
#   wikidata: the matched Wikidata QID, when captured
#   retrieved: UTC date (YYYY-MM-DD) the blurb was fetched
`

// CitiesFile mirrors the subset of seeds/cities.yaml this tool needs.
type CitiesFile struct {
	Cities []City `yaml:"cities"`
}

type City struct {
	Key       string   `yaml:"key"`
	Name      string   `yaml:"name"`
	Tier      int      `yaml:"tier"`
	Lat       *float64 `yaml:"lat"`
	Lng       *float64 `yaml:"lng"`
	GeonameID string   `yaml:"geoname_id"`
	AltNames  []string `yaml:"alt_names"`
}

// FactsFile is the schema of seeds/city_facts.yaml.
type FactsFile struct {
	Facts []CityFact `yaml:"facts"`
}

type CityFact struct {
	Key         string `yaml:"key"`
	Blurb       string `yaml:"blurb"`
	SourceTitle string `yaml:"source_title"`
	SourceURL   string `yaml:"source_url"`
	Wikidata    string `yaml:"wikidata,omitempty"`
	Retrieved   string `yaml:"retrieved"`
}

func main() {
	citiesPath := flag.String("cities", "seeds/cities.yaml", "path to seeds/cities.yaml")
	outPath := flag.String("out", "seeds/city_facts.yaml", "path to output seeds/city_facts.yaml")
	tierMax := flag.Int("tier-max", 2, "only scrape cities with tier <= this")
	limit := flag.Int("limit", 0, "cap the number of cities processed this run (0 = all)")
	rps := flag.Float64("rps", 2, "max HTTP requests per second")
	force := flag.Bool("force", false, "re-scrape keys already present in -out")
	flag.Parse()

	if *rps <= 0 {
		*rps = 2
	}

	cf, err := loadCities(*citiesPath)
	if err != nil {
		log.Fatalf("cityfacts: load cities: %v", err)
	}

	ff, err := loadFacts(*outPath)
	if err != nil {
		log.Fatalf("cityfacts: load existing facts: %v", err)
	}
	byKey := make(map[string]CityFact, len(ff.Facts))
	for _, f := range ff.Facts {
		byKey[f.Key] = f
	}

	var candidates []City
	for _, c := range cf.Cities {
		if c.Tier <= *tierMax {
			candidates = append(candidates, c)
		}
	}

	var toProcess []City
	skipped := 0
	for _, c := range candidates {
		if _, ok := byKey[c.Key]; ok && !*force {
			skipped++
			continue
		}
		toProcess = append(toProcess, c)
	}
	if *limit > 0 && len(toProcess) > *limit {
		toProcess = toProcess[:*limit]
	}

	fmt.Printf("cityfacts: %d candidates (tier<=%d), %d already present (skipped), %d to process this run\n",
		len(candidates), *tierMax, skipped, len(toProcess))

	client := &http.Client{Timeout: 30 * time.Second}
	ticker := time.NewTicker(time.Duration(float64(time.Second) / *rps))
	defer ticker.Stop()

	// Step 1: batch-resolve geoname_id -> Wikidata match via SPARQL.
	wdMatches := make(map[string][]wdMatch)
	var gids []string
	seenGid := make(map[string]bool)
	for _, c := range toProcess {
		if c.GeonameID != "" && !seenGid[c.GeonameID] {
			seenGid[c.GeonameID] = true
			gids = append(gids, c.GeonameID)
		}
	}
	const batchSize = 150
	for i := 0; i < len(gids); i += batchSize {
		end := i + batchSize
		if end > len(gids) {
			end = len(gids)
		}
		batch := gids[i:end]
		<-ticker.C
		m, err := queryWikidataBatch(client, batch)
		if err != nil {
			fmt.Printf("cityfacts: wikidata batch %d-%d failed: %v (falling back to geosearch for this batch)\n", i, end, err)
			continue
		}
		for k, v := range m {
			wdMatches[k] = append(wdMatches[k], v...)
		}
	}

	successCount := 0
	var unmatchedKeys []string

	tryStore := func(c City, title, qid string) bool {
		<-ticker.C
		summary, err := fetchPageSummary(client, title)
		if err != nil {
			fmt.Printf("cityfacts: summary fetch failed for %s (%s): %v\n", c.Key, title, err)
			return false
		}
		usedTitle := title
		if isDisambiguation(summary.Type) {
			if c.Lat == nil || c.Lng == nil {
				return false
			}
			<-ticker.C
			results, gerr := geoSearch(client, *c.Lat, *c.Lng)
			if gerr != nil {
				return false
			}
			altTitle, ok := selectGeoSearchMatch(results, c.Name, c.AltNames)
			if !ok || altTitle == usedTitle {
				return false
			}
			<-ticker.C
			summary2, err2 := fetchPageSummary(client, altTitle)
			if err2 != nil || isDisambiguation(summary2.Type) {
				return false
			}
			usedTitle = altTitle
			summary = summary2
			qid = "" // no longer a Wikidata-confirmed match
		}
		if summary.Title != "" {
			usedTitle = summary.Title // canonical title, resolves redirects
		}
		blurb := trimBlurb(summary.Extract)
		if blurb == "" {
			return false
		}
		sourceURL := summary.ContentUrls.Desktop.Page
		if sourceURL == "" {
			sourceURL = "https://en.wikipedia.org/wiki/" + strings.ReplaceAll(usedTitle, " ", "_")
		}
		byKey[c.Key] = CityFact{
			Key:         c.Key,
			Blurb:       blurb,
			SourceTitle: usedTitle,
			SourceURL:   sourceURL,
			Wikidata:    qid,
			Retrieved:   time.Now().UTC().Format("2006-01-02"),
		}
		successCount++
		if successCount%25 == 0 {
			if werr := writeFacts(*outPath, byKey); werr != nil {
				fmt.Printf("cityfacts: intermediate write failed: %v\n", werr)
			} else {
				fmt.Printf("cityfacts: checkpoint written (%d new so far)\n", successCount)
			}
		}
		return true
	}

	for _, c := range toProcess {
		var title, qid string
		matched := false
		if c.GeonameID != "" {
			if cands, ok := wdMatches[c.GeonameID]; ok {
				if best, ok2 := pickWikidataMatch(cands, c.Name, c.AltNames); ok2 {
					title, qid, matched = best.Title, best.QID, true
				}
			}
		}
		if !matched {
			// A GeoNames ID mismatch between our seed data and Wikidata's
			// own P1566 value is common (GeoNames sometimes has more than
			// one record for the same city), so before falling back to
			// coordinate-based GeoSearch — which can surface a landmark
			// sitting right at the city's centroid instead of the city's
			// own article — try the city's own name (and alt_names)
			// directly as a Wikipedia title.
			if t, ok := tryDirectNameMatch(client, ticker, c.Name, c.AltNames); ok {
				title = t
				matched = true
			}
		}
		if !matched {
			if c.Lat == nil || c.Lng == nil {
				unmatchedKeys = append(unmatchedKeys, c.Key)
				continue
			}
			<-ticker.C
			results, err := geoSearch(client, *c.Lat, *c.Lng)
			if err != nil {
				fmt.Printf("cityfacts: geosearch failed for %s: %v\n", c.Key, err)
				unmatchedKeys = append(unmatchedKeys, c.Key)
				continue
			}
			t, ok := selectGeoSearchMatch(results, c.Name, c.AltNames)
			if !ok {
				unmatchedKeys = append(unmatchedKeys, c.Key)
				continue
			}
			title = t
		}
		if !tryStore(c, title, qid) {
			unmatchedKeys = append(unmatchedKeys, c.Key)
		}
	}

	if err := writeFacts(*outPath, byKey); err != nil {
		log.Fatalf("cityfacts: final write failed: %v", err)
	}

	sort.Strings(unmatchedKeys)
	fmt.Println("=== cityfacts summary ===")
	fmt.Printf("matched:   %d\n", successCount)
	fmt.Printf("unmatched: %d\n", len(unmatchedKeys))
	fmt.Printf("skipped:   %d (already present in -out)\n", skipped)
	if len(unmatchedKeys) > 0 {
		fmt.Println("unmatched keys:")
		for _, k := range unmatchedKeys {
			fmt.Println("  -", k)
		}
	}
}

// --- file I/O ---

func loadCities(path string) (*CitiesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf CitiesFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, err
	}
	return &cf, nil
}

func loadFacts(path string) (*FactsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FactsFile{}, nil
		}
		return nil, err
	}
	var ff FactsFile
	if err := yaml.Unmarshal(data, &ff); err != nil {
		return nil, err
	}
	return &ff, nil
}

func marshalFacts(list []CityFact) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(FactsFile{Facts: list}); err != nil {
		enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeFacts(path string, byKey map[string]CityFact) error {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := make([]CityFact, 0, len(keys))
	for _, k := range keys {
		list = append(list, byKey[k])
	}
	body, err := marshalFacts(list)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(factsFileHeader)
	buf.Write(body)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- Wikidata ---

type wdMatch struct {
	Title string
	QID   string
}

type wdResponse struct {
	Results struct {
		Bindings []struct {
			Gid struct {
				Value string `json:"value"`
			} `json:"gid"`
			Item struct {
				Value string `json:"value"`
			} `json:"item"`
			Article struct {
				Value string `json:"value"`
			} `json:"article"`
		} `json:"bindings"`
	} `json:"results"`
}

func buildSparqlQuery(gids []string) string {
	var vals strings.Builder
	for _, g := range gids {
		vals.WriteString(`"`)
		vals.WriteString(g)
		vals.WriteString(`" `)
	}
	return fmt.Sprintf(`SELECT ?gid ?item ?article WHERE { VALUES ?gid { %s} ?item wdt:P1566 ?gid . ?article schema:about ?item ; schema:isPartOf <https://en.wikipedia.org/> . }`, vals.String())
}

// queryWikidataBatch resolves a batch of GeoNames IDs to Wikidata matches.
// A single GeoNames ID can legitimately (or erroneously, per real-world
// Wikidata data quality) resolve to more than one item/article, so every
// candidate is returned — callers must disambiguate (see
// pickWikidataMatch) rather than assume the first/last binding is correct.
func queryWikidataBatch(client *http.Client, gids []string) (map[string][]wdMatch, error) {
	q := buildSparqlQuery(gids)
	u := "https://query.wikidata.org/sparql?format=json&query=" + url.QueryEscape(q)
	body, err := httpGetWithRetry(client, u, "application/sparql-results+json")
	if err != nil {
		return nil, err
	}
	var parsed wdResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	result := make(map[string][]wdMatch)
	for _, b := range parsed.Results.Bindings {
		title, terr := extractWikipediaTitle(b.Article.Value)
		if terr != nil {
			continue
		}
		result[b.Gid.Value] = append(result[b.Gid.Value], wdMatch{Title: title, QID: extractQID(b.Item.Value)})
	}
	return result, nil
}

// pickWikidataMatch disambiguates among the candidate Wikidata matches for
// one GeoNames ID. If exactly one candidate came back, it's used as-is
// (the overwhelmingly common case). If there are several, only one whose
// article title casefold-matches the city's name/alt_names is trusted;
// otherwise the GeoNames ID is treated as unresolved so the caller falls
// back to GeoSearch instead of risking an unrelated article (e.g. a
// hospital that happens to share a stray P1566 value with the city).
func pickWikidataMatch(cands []wdMatch, name string, altNames []string) (wdMatch, bool) {
	if len(cands) == 1 {
		return cands[0], true
	}
	for _, cand := range cands {
		if matchesCityName(cand.Title, name, altNames) {
			return cand, true
		}
	}
	return wdMatch{}, false
}

func extractWikipediaTitle(articleURL string) (string, error) {
	u, err := url.Parse(articleURL)
	if err != nil {
		return "", err
	}
	const prefix = "/wiki/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", fmt.Errorf("unexpected article path: %s", u.Path)
	}
	raw := strings.TrimPrefix(u.Path, prefix)
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(decoded, "_", " "), nil
}

func extractQID(itemURL string) string {
	idx := strings.LastIndex(itemURL, "/")
	if idx < 0 || idx+1 >= len(itemURL) {
		return ""
	}
	return itemURL[idx+1:]
}

// --- Wikipedia GeoSearch fallback ---

type geoSearchResult struct {
	Title string
	Dist  float64
}

func geoSearch(client *http.Client, lat, lng float64) ([]geoSearchResult, error) {
	u := fmt.Sprintf("https://en.wikipedia.org/w/api.php?action=query&list=geosearch&gscoord=%g|%g&gsradius=10000&gslimit=5&format=json", lat, lng)
	body, err := httpGetWithRetry(client, u, "application/json")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Query struct {
			Geosearch []struct {
				Title string  `json:"title"`
				Dist  float64 `json:"dist"`
			} `json:"geosearch"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]geoSearchResult, 0, len(parsed.Query.Geosearch))
	for _, g := range parsed.Query.Geosearch {
		out = append(out, geoSearchResult{Title: g.Title, Dist: g.Dist})
	}
	return out, nil
}

// tryDirectNameMatch tries the city's own name, then each alt_name, as a
// literal Wikipedia article title. This is the highest-precision path when
// there's no (trustworthy) Wikidata match: a coordinate-based GeoSearch
// often surfaces a landmark sitting right at a city's centroid instead of
// the city's own article, whereas the plain English name usually *is* the
// city's article title.
func tryDirectNameMatch(client *http.Client, ticker *time.Ticker, name string, altNames []string) (string, bool) {
	candidates := append([]string{name}, altNames...)
	seen := make(map[string]bool, len(candidates))
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand == "" || seen[cand] {
			continue
		}
		seen[cand] = true
		<-ticker.C
		summary, err := fetchPageSummary(client, cand)
		if err != nil {
			continue
		}
		if isDisambiguation(summary.Type) || strings.TrimSpace(summary.Extract) == "" {
			continue
		}
		title := summary.Title
		if title == "" {
			title = cand
		}
		return title, true
	}
	return "", false
}

// selectGeoSearchMatch picks the best geosearch result for a city: an exact
// casefold match against the city's name or one of its alt_names, else the
// nearest result (results arrive sorted by distance) whose title contains
// the city name, else no match.
func selectGeoSearchMatch(results []geoSearchResult, name string, altNames []string) (string, bool) {
	for _, r := range results {
		if matchesCityName(r.Title, name, altNames) {
			return r.Title, true
		}
	}
	for _, r := range results {
		if titleContainsCityName(r.Title, name) {
			return r.Title, true
		}
	}
	return "", false
}

// matchesCityName reports whether title casefold-matches name or one of
// altNames.
func matchesCityName(title, name string, altNames []string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == strings.ToLower(strings.TrimSpace(name)) {
		return true
	}
	for _, a := range altNames {
		if t == strings.ToLower(strings.TrimSpace(a)) {
			return true
		}
	}
	return false
}

func titleContainsCityName(title, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	return strings.Contains(strings.ToLower(title), strings.ToLower(name))
}

// --- Wikipedia page summary ---

type pageSummary struct {
	Title       string `json:"title"`
	Type        string `json:"type"`
	Extract     string `json:"extract"`
	ContentUrls struct {
		Desktop struct {
			Page string `json:"page"`
		} `json:"desktop"`
	} `json:"content_urls"`
}

func fetchPageSummary(client *http.Client, title string) (*pageSummary, error) {
	encodedTitle := url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	u := "https://en.wikipedia.org/api/rest_v1/page/summary/" + encodedTitle
	body, err := httpGetWithRetry(client, u, "application/json")
	if err != nil {
		return nil, err
	}
	var ps pageSummary
	if err := json.Unmarshal(body, &ps); err != nil {
		return nil, err
	}
	return &ps, nil
}

// isDisambiguation reports whether a page summary's type marks it as a
// disambiguation page (no usable blurb).
func isDisambiguation(pageType string) bool {
	return pageType == "disambiguation"
}

// --- HTTP with politeness (User-Agent, retry on 429/5xx) ---

func httpGetWithRetry(client *http.Client, u, accept string) ([]byte, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	const maxAttempts = 4 // 1 initial + 3 retries
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case rerr != nil:
				lastErr = rerr
			case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
				lastErr = fmt.Errorf("retryable status %d from %s", resp.StatusCode, u)
			case resp.StatusCode != http.StatusOK:
				return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, u, string(body))
			default:
				return body, nil
			}
		}
		if attempt < maxAttempts-1 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

// --- blurb trimming ---

// commonAbbreviations lists (dot-stripped, lowercased) tokens that
// frequently precede a "." in Wikipedia lead paragraphs without ending the
// sentence (e.g. "St. Petersburg", "the U.S. state of ..."), so splitting
// naively on every period badly mangles real extracts.
var commonAbbreviations = map[string]bool{
	"st": true, "mt": true, "ft": true, "mr": true, "mrs": true, "ms": true,
	"dr": true, "jr": true, "sr": true, "vs": true, "etc": true, "us": true,
	"uk": true, "un": true, "no": true, "prof": true, "rev": true, "gen": true,
	"col": true, "sen": true, "rep": true, "al": true, "cf": true, "eg": true,
	"ie": true, "approx": true, "co": true, "corp": true, "inc": true,
	"ltd": true, "dept": true, "ave": true, "blvd": true, "capt": true,
	"cmdr": true,
}

// abbreviationBefore reports whether the token immediately preceding
// runes[periodIdx] (a '.') is a known abbreviation, so that period should
// not be treated as a sentence boundary.
func abbreviationBefore(runes []rune, periodIdx int) bool {
	j := periodIdx
	for j > 0 && (unicode.IsLetter(runes[j-1]) || runes[j-1] == '.') {
		j--
	}
	token := strings.ToLower(strings.ReplaceAll(string(runes[j:periodIdx]), ".", ""))
	return commonAbbreviations[token]
}

// singleLetterAcronymPeriod reports whether runes[i] is a '.' that's part
// of a single-letter acronym like "U.S." or "U.K." — i.e. immediately
// preceded by exactly one uppercase letter that is itself preceded by
// start-of-string, whitespace, an opening bracket/quote, or another '.'.
// Such periods are essentially never real sentence boundaries.
func singleLetterAcronymPeriod(runes []rune, i int) bool {
	if i == 0 || !unicode.IsUpper(runes[i-1]) {
		return false
	}
	if i-2 < 0 {
		return true
	}
	before := runes[i-2]
	return before == '.' || unicode.IsSpace(before) || before == '(' || before == '"' || before == '\''
}

// splitSentences splits s into sentences on '.', '!', and '?', with two
// guards against Wikipedia's most common false-positive splits: a '.'
// between two digits (a decimal number, e.g. "12.5 million") is never a
// boundary, and a '.' that's a known abbreviation or single-letter acronym
// (e.g. "St.", "U.S.") is only treated as a boundary once the surrounding
// heuristics agree it truly ends the sentence.
func splitSentences(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	runes := []rune(s)
	n := len(runes)
	var sentences []string
	start := 0
	i := 0
	for i < n {
		c := runes[i]
		if c != '.' && c != '!' && c != '?' {
			i++
			continue
		}
		if c == '.' && i+1 < n && unicode.IsDigit(runes[i+1]) && i > 0 && unicode.IsDigit(runes[i-1]) {
			i++ // decimal number, e.g. "12.5" — never a boundary
			continue
		}
		if c == '.' && (singleLetterAcronymPeriod(runes, i) || abbreviationBefore(runes, i)) {
			i++
			continue
		}
		// Extend over consecutive terminators (e.g. "...", "?!").
		j := i + 1
		for j < n && (runes[j] == '.' || runes[j] == '!' || runes[j] == '?') {
			j++
		}
		// Allow a trailing closing quote/paren before the whitespace check.
		k := j
		for k < n && strings.ContainsRune(`"')’”`, runes[k]) {
			k++
		}
		boundary := false
		switch {
		case k >= n:
			boundary = true
		case unicode.IsSpace(runes[k]):
			m := k
			for m < n && unicode.IsSpace(runes[m]) {
				m++
			}
			boundary = m >= n || unicode.IsUpper(runes[m]) || unicode.IsDigit(runes[m])
		}
		if !boundary {
			i = j
			continue
		}
		sentence := strings.TrimSpace(string(runes[start:k]))
		if sentence != "" {
			sentences = append(sentences, sentence)
		}
		start = k
		for start < n && unicode.IsSpace(runes[start]) {
			start++
		}
		i = start
	}
	if start < n {
		remainder := strings.TrimSpace(string(runes[start:]))
		if remainder != "" {
			sentences = append(sentences, remainder)
		}
	}
	return sentences
}

// trimBlurb trims a Wikipedia summary extract to at most 2 sentences AND at
// most 350 characters, ending on a sentence boundary where possible.
func trimBlurb(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	sentences := splitSentences(s)
	if len(sentences) == 0 {
		return s
	}
	n := len(sentences)
	if n > 2 {
		n = 2
	}
	for n >= 1 {
		candidate := strings.TrimSpace(strings.Join(sentences[:n], " "))
		if len(candidate) <= 350 {
			return candidate
		}
		n--
	}
	// Even the first sentence alone exceeds 350 chars: hard-truncate at the
	// last word boundary within the limit.
	first := strings.TrimSpace(sentences[0])
	if len(first) <= 350 {
		return first
	}
	cut := first[:350]
	if idx := strings.LastIndexAny(cut, " "); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, " ,;:-")
}

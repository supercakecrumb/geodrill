package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// preloadedRe extracts the JSON payload plonkit.net's server-side render
// embeds in every guide page's <head>:
//
//	<script id="__PRELOADED_DATA__" type="application/json">{...}</script>
//
// Discovered 2026-07-18 by diffing a plain GET of a guide page against its
// rendered DOM: plonkit is a client-rendered React SPA (a bare <div id=
// "root"> ships in the base bundle), but each guide route is pre-rendered
// server-side with this script tag carrying the exact data the client
// hydrates from — so no headless browser is needed to scrape it, a plain
// GET is enough. The non-greedy ".*?" combined with requiring the literal
// "</script>" suffix correctly finds the true end of the (deeply nested)
// JSON object: every "}" inside the payload is followed by more JSON, not by
// "</script>", so only the real closing brace lets the match succeed.
var preloadedRe = regexp.MustCompile(`(?s)<script id="__PRELOADED_DATA__" type="application/json">\s*(\{.*?\})\s*</script>`)

// preloadedResponse is the top-level shape of the __PRELOADED_DATA__ JSON.
type preloadedResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Public        guidePayload `json:"public"`
		CommunityDocs []string     `json:"communityDocs"`
	} `json:"data"`
}

// guidePayload is one country/region guide, exactly as plonkit.net's own
// backend shapes it (field names verified against the live Netherlands,
// Germany, France, United Kingdom, Japan, and Poland guides on 2026-07-18).
type guidePayload struct {
	ID        string   `json:"_id"`
	Title     string   `json:"title"`
	Slug      string   `json:"slug"`
	Code      string   `json:"code"` // ISO 3166-1 alpha-2, e.g. "NL"
	Cat       []string `json:"cat"`  // world region(s), e.g. ["Europe"]
	HeroImage string   `json:"heroImage"`
	Steps     []step   `json:"steps"`
}

// step is one top-level section of a guide. Kind "tip" is a normal
// tips-with-heading section (Items populated); kind "map" is the closing
// "Maps and resources" section (Text populated, no Items) — every sample
// country had exactly three "tip" steps followed by one "map" step.
type step struct {
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	Items []item   `json:"items,omitempty"`
	Text  []string `json:"text,omitempty"`
}

// item is one entry within a "tip"-kind step. Kind "tip" is real content
// (Data populated, optionally Tags); kind "centeredImage" is a standalone
// illustration with no text/category; kind "divider" is a pure visual
// separator with neither — both are dropped by buildCountryDoc (see its doc
// comment).
type item struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	ImageURL string   `json:"imageUrl,omitempty"` // only for kind=centeredImage
	Data     *tipData `json:"data,omitempty"`     // only for kind=tip
	Tags     []string `json:"tags,omitempty"`     // only for kind=tip; most tips have none
}

// tipData is a tip's content: one illustrating image plus one or more
// markdown-flavoured text paragraphs (bold **like this**, links [text]
// (url), and editorial "NOTE: ..." asides are all inline in Text, not
// separately structured — passed through as-is, not rendered).
type tipData struct {
	Image tipImage `json:"image"`
	Text  []string `json:"text"`
}

// tipImage is a tip's illustrating image. ImageURL is almost always a
// site-relative path (e.g. "/images/netherlands/x.png"); ImageLink is
// usually an external Google Maps Street View link documenting where the
// photo was taken, but sometimes duplicates ImageURL (Full-size image, no
// map reference available).
type tipImage struct {
	ImageURL  string  `json:"imageUrl"`
	ImageLink string  `json:"imageLink"`
	Alt       string  `json:"alt"`
	Width     float64 `json:"width"`
}

// extractPreloadedJSON pulls the raw __PRELOADED_DATA__ JSON out of a guide
// page's HTML. See preloadedRe's doc comment for how the page is shaped.
func extractPreloadedJSON(html []byte) ([]byte, error) {
	m := preloadedRe.FindSubmatch(html)
	if m == nil {
		return nil, fmt.Errorf("__PRELOADED_DATA__ script tag not found — page structure may have changed")
	}
	return m[1], nil
}

// parseGuidePage extracts and decodes a guide page's preloaded JSON.
func parseGuidePage(html []byte) (guidePayload, error) {
	raw, err := extractPreloadedJSON(html)
	if err != nil {
		return guidePayload{}, err
	}
	var resp preloadedResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return guidePayload{}, fmt.Errorf("parse __PRELOADED_DATA__ json: %w", err)
	}
	if !resp.Success {
		return guidePayload{}, fmt.Errorf("plonkit responded success=false")
	}
	if resp.Data.Public.Code == "" {
		return guidePayload{}, fmt.Errorf("parsed guide payload has no country code — unexpected shape")
	}
	return resp.Data.Public, nil
}

// --- Output (seed-shaped) types -------------------------------------------

// countryDoc is the per-country YAML file shape written under -out
// (task brief: "seed-shaped: country ISO code, categories, tips").
type countryDoc struct {
	Country          countryMeta     `yaml:"country"`
	Sections         []sectionOutput `yaml:"sections"`
	MapsAndResources []string        `yaml:"maps_and_resources,omitempty"`
}

type countryMeta struct {
	ISOA2     string    `yaml:"iso_a2"`
	Name      string    `yaml:"name"`
	Slug      string    `yaml:"slug"`
	Region    string    `yaml:"region,omitempty"`
	SourceURL string    `yaml:"source_url"`
	ScrapedAt time.Time `yaml:"scraped_at"`
}

type sectionOutput struct {
	Title string      `yaml:"title"`
	Tips  []tipOutput `yaml:"tips"`
}

type tipOutput struct {
	ID         string   `yaml:"id"`
	Categories []string `yaml:"categories,omitempty"`
	Text       []string `yaml:"text"`
	ImageURL   string   `yaml:"image_url,omitempty"`
	ImageLink  string   `yaml:"image_link,omitempty"`
}

// buildCountryDoc transforms a parsed guidePayload into the seed-shaped
// output. "centeredImage" and "divider" items carry no tip text/category
// (see item's doc comment) so the brief's "each tip with: category, text
// summary, image URLs" shape doesn't fit them — they are dropped here, a
// documented prototype limitation (see design doc); a future pass could fold
// centeredImage in as a section-level illustration list.
func buildCountryDoc(iso, slug string, g guidePayload, scrapedAt time.Time) countryDoc {
	doc := countryDoc{
		Country: countryMeta{
			ISOA2:     iso,
			Name:      g.Title,
			Slug:      slug,
			Region:    strings.Join(g.Cat, ", "),
			SourceURL: siteBaseURL + "/" + slug,
			ScrapedAt: scrapedAt,
		},
	}
	for _, st := range g.Steps {
		switch st.Kind {
		case "tip":
			sec := sectionOutput{Title: st.Title}
			for _, it := range st.Items {
				if it.Kind != "tip" || it.Data == nil {
					continue
				}
				sec.Tips = append(sec.Tips, tipOutput{
					ID:         it.ID,
					Categories: it.Tags,
					Text:       it.Data.Text,
					ImageURL:   resolveImageURL(it.Data.Image.ImageURL),
					ImageLink:  it.Data.Image.ImageLink,
				})
			}
			doc.Sections = append(doc.Sections, sec)
		case "map":
			doc.MapsAndResources = append(doc.MapsAndResources, st.Text...)
		}
	}
	return doc
}

// resolveImageURL absolutizes plonkit's site-relative image paths; already
// fully-qualified URLs pass through unchanged.
func resolveImageURL(u string) string {
	if u == "" || strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return siteBaseURL + u
}

package main

import (
	"os"
	"testing"
	"time"
)

// TestParseGuidePage_Netherlands exercises the parser against a real (but
// trimmed — see testdata/netherlands.html's provenance note in the design
// doc) capture of plonkit.net's Netherlands guide page: 5 tips across 3
// tip-sections (one tagged "bollard", one "pole", two untagged, one
// centeredImage dropped) plus a trimmed "map" section, so every schema shape
// buildCountryDoc handles gets exercised.
func TestParseGuidePage_Netherlands(t *testing.T) {
	html, err := os.ReadFile("testdata/netherlands.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	guide, err := parseGuidePage(html)
	if err != nil {
		t.Fatalf("parseGuidePage: %v", err)
	}

	if guide.Code != "NL" {
		t.Errorf("Code = %q, want NL", guide.Code)
	}
	if guide.Title != "Netherlands" {
		t.Errorf("Title = %q, want Netherlands", guide.Title)
	}
	if guide.Slug != "netherlands" {
		t.Errorf("Slug = %q, want netherlands", guide.Slug)
	}
	if len(guide.Cat) != 1 || guide.Cat[0] != "Europe" {
		t.Errorf("Cat = %v, want [Europe]", guide.Cat)
	}

	wantSteps := []struct {
		kind  string
		title string
		items int // len(Items) for kind=tip, len(Text) for kind=map
	}{
		{"tip", "Identifying the Netherlands", 5}, // 1 centeredImage + 4 tips
		{"tip", "Regional and province-specific clues", 1},
		{"tip", "Spotlight", 1},
		{"map", "Maps and resources", 2},
	}
	if len(guide.Steps) != len(wantSteps) {
		t.Fatalf("len(Steps) = %d, want %d", len(guide.Steps), len(wantSteps))
	}
	for i, want := range wantSteps {
		got := guide.Steps[i]
		if got.Kind != want.kind || got.Title != want.title {
			t.Errorf("Steps[%d] = {%q,%q}, want {%q,%q}", i, got.Kind, got.Title, want.kind, want.title)
		}
		n := len(got.Items)
		if got.Kind == "map" {
			n = len(got.Text)
		}
		if n != want.items {
			t.Errorf("Steps[%d] item/text count = %d, want %d", i, n, want.items)
		}
	}

	doc := buildCountryDoc("NL", "netherlands", guide, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC))

	if doc.Country.ISOA2 != "NL" || doc.Country.Name != "Netherlands" || doc.Country.SourceURL != "https://www.plonkit.net/netherlands" {
		t.Errorf("Country meta = %+v", doc.Country)
	}

	// buildCountryDoc drops the centeredImage item, so the first section
	// should have 4 tips, not 5.
	if len(doc.Sections) != 3 {
		t.Fatalf("len(Sections) = %d, want 3", len(doc.Sections))
	}
	if len(doc.Sections[0].Tips) != 4 {
		t.Errorf("Sections[0].Tips count = %d, want 4 (centeredImage dropped)", len(doc.Sections[0].Tips))
	}

	// R6It is tagged "bollard" with a site-relative image absolutized.
	var bollardTip *tipOutput
	for i := range doc.Sections[0].Tips {
		if doc.Sections[0].Tips[i].ID == "R6It" {
			bollardTip = &doc.Sections[0].Tips[i]
		}
	}
	if bollardTip == nil {
		t.Fatal("tip R6It not found")
	}
	if len(bollardTip.Categories) != 1 || bollardTip.Categories[0] != "bollard" {
		t.Errorf("R6It Categories = %v, want [bollard]", bollardTip.Categories)
	}
	if bollardTip.ImageURL != "https://www.plonkit.net/images/netherlands/Dutch_bollard.png" {
		t.Errorf("R6It ImageURL = %q, want absolutized site-relative path", bollardTip.ImageURL)
	}
	if bollardTip.ImageLink != "https://goo.gl/maps/x8RkQr3mT4Q6H5pG6" {
		t.Errorf("R6It ImageLink = %q, want external Street View link unchanged", bollardTip.ImageLink)
	}

	// SU02 (license plate tip) carries no tags — untagged.
	su02 := doc.Sections[0].Tips[0]
	if su02.ID != "SU02" || len(su02.Categories) != 0 {
		t.Errorf("SU02 = %+v, want untagged first tip", su02)
	}

	if len(doc.MapsAndResources) != 2 {
		t.Errorf("MapsAndResources count = %d, want 2", len(doc.MapsAndResources))
	}
}

func TestExtractPreloadedJSON_MissingTag(t *testing.T) {
	if _, err := extractPreloadedJSON([]byte("<html><body>no data here</body></html>")); err == nil {
		t.Fatal("expected error for missing __PRELOADED_DATA__ tag, got nil")
	}
}

func TestTagCounter(t *testing.T) {
	doc := countryDoc{
		Sections: []sectionOutput{
			{Tips: []tipOutput{
				{ID: "a", Categories: []string{"bollard"}},
				{ID: "b", Categories: []string{"bollard", "pole"}},
				{ID: "c"}, // untagged
			}},
		},
	}
	c := newTagCounter()
	c.add(doc)
	tax := c.build([]string{"NL"})

	counts := make(map[string]int, len(tax.Categories))
	for _, cc := range tax.Categories {
		counts[cc.Key] = cc.Count
	}
	if counts["bollard"] != 2 {
		t.Errorf("bollard count = %d, want 2", counts["bollard"])
	}
	if counts["pole"] != 1 {
		t.Errorf("pole count = %d, want 1", counts["pole"])
	}
	if counts[untaggedKey] != 1 {
		t.Errorf("%s count = %d, want 1", untaggedKey, counts[untaggedKey])
	}
	if len(tax.UnknownTags) != 0 {
		t.Errorf("UnknownTags = %v, want none (bollard/pole are both known)", tax.UnknownTags)
	}
}

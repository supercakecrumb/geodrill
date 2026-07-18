package roadside_test

// Integration test against a real PostgreSQL 18, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring
// internal/storage/integration_test.go). Seeds a fresh schema and asserts
// the design §6.2 audit invariants: country counts, exactly one drives_on
// fact per country, item payload/fact consistency, GB-subdivision
// parenting, and a handful of spot checks.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name
// contains "test" — see testDSN's safety fuse, copied from
// internal/storage/integration_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run roadside integration tests")
	}
	if name := databaseName(dsn); !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run destructive integration tests against database %q: "+
			"GEODRILL_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\" "+
			"(e.g. geodrill_test), never the live database", name)
	}
	return dsn
}

func databaseName(dsn string) string {
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

func freshSchema(t *testing.T, dsn string) {
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

func TestSeedAndConsistency(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Idempotency: reseeding must converge, not duplicate or error.
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("reseed: %v", err)
	}

	countries, err := store.ListCountries(ctx)
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	if len(countries) != 252 {
		t.Fatalf("len(countries) = %d, want 252", len(countries))
	}

	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	var unCount int
	for _, c := range countries {
		countryByID[c.ID] = c
		if c.UNMember {
			unCount++
		}
	}
	if unCount != 193 {
		t.Fatalf("un_member count = %d, want 193", unCount)
	}

	// GB subdivisions parented to GB.
	gb, found, err := store.GetCountryByISO(ctx, "GB")
	if err != nil || !found {
		t.Fatalf("get GB: found=%v err=%v", found, err)
	}
	for _, code := range []string{"GB-ENG", "GB-SCT", "GB-WLS"} {
		c, found, err := store.GetCountryByISO(ctx, code)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", code, found, err)
		}
		if c.ParentCountryID == nil || *c.ParentCountryID != gb.ID {
			t.Fatalf("country %s parent_country_id = %v, want %v (GB)", code, c.ParentCountryID, gb.ID)
		}
		if !c.IsSubdivision {
			t.Fatalf("country %s is_subdivision = false, want true", code)
		}
	}

	// Exactly one drives_on fact per country.
	drivesOnDef, found, err := store.GetFactDefByKey(ctx, roadside.FactKeyDrivesOn)
	if err != nil || !found {
		t.Fatalf("get drives_on fact def: found=%v err=%v", found, err)
	}
	facts, err := store.ListCountryFactsByDefKey(ctx, roadside.FactKeyDrivesOn)
	if err != nil {
		t.Fatalf("list drives_on facts: %v", err)
	}
	if len(facts) != len(countries) {
		t.Fatalf("len(drives_on facts) = %d, want %d (one per country)", len(facts), len(countries))
	}
	factCountByCountry := make(map[uuid.UUID]int, len(countries))
	factSideByCountry := make(map[uuid.UUID]string, len(countries))
	for _, f := range facts {
		if f.FactDefID != drivesOnDef.ID {
			t.Fatalf("fact %+v has unexpected fact_def_id", f)
		}
		factCountByCountry[f.CountryID]++
		if f.ValText == nil {
			t.Fatalf("drives_on fact for country %v has nil val_text", f.CountryID)
		}
		factSideByCountry[f.CountryID] = *f.ValText
	}
	for _, c := range countries {
		if n := factCountByCountry[c.ID]; n != 1 {
			t.Fatalf("country %s (%s) has %d drives_on facts, want exactly 1", c.Name, c.ISOA2, n)
		}
	}

	// Topic + items.
	topic, found, err := store.GetTopicByPath(ctx, roadside.RootSlug+"/"+roadside.TopicSlug)
	if err != nil || !found {
		t.Fatalf("get topic roads/which-side: found=%v err=%v", found, err)
	}
	if topic.QuizKind != "road_side" {
		t.Fatalf("topic quiz_kind = %q, want %q", topic.QuizKind, "road_side")
	}
	items, err := store.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != len(countries) {
		t.Fatalf("len(items) = %d, want %d (one per country)", len(items), len(countries))
	}

	// Every item payload's side matches its country's drives_on fact
	// (design §6.2 audit — payload is a cache and MUST match).
	for _, it := range items {
		if it.CountryID == nil {
			t.Fatalf("item %s has no country_id", it.Key)
		}
		country, ok := countryByID[*it.CountryID]
		if !ok {
			t.Fatalf("item %s references unknown country_id %v", it.Key, *it.CountryID)
		}
		var payload struct {
			Side string `json:"side"`
		}
		if err := json.Unmarshal(it.Payload, &payload); err != nil {
			t.Fatalf("item %s: unmarshal payload: %v", it.Key, err)
		}
		wantSide := factSideByCountry[country.ID]
		wantCode := "L"
		if wantSide == "right" {
			wantCode = "R"
		}
		if payload.Side != wantCode {
			t.Fatalf("item %s (%s) payload.side = %q, want %q (fact says %q)", it.Key, country.Name, payload.Side, wantCode, wantSide)
		}
		if it.Key != country.ISOA2 {
			t.Fatalf("item key = %q, want country iso_a2 %q", it.Key, country.ISOA2)
		}
	}

	// Arbitrary-filter sanity (architecture §2.7): countries that drive on
	// the left AND are UN members.
	rows, err := store.Pool().Query(ctx, `
		SELECT c.name FROM countries c
		JOIN country_facts cf ON cf.country_id = c.id
		JOIN fact_defs fd ON fd.id = cf.fact_def_id AND fd.key = $1
		WHERE cf.val_text = 'left' AND c.un_member = true
		ORDER BY c.name`, roadside.FactKeyDrivesOn)
	if err != nil {
		t.Fatalf("arbitrary filter join: %v", err)
	}
	var leftUNNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		leftUNNames = append(leftUNNames, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows err: %v", err)
	}
	rows.Close()
	if len(leftUNNames) == 0 {
		t.Fatal("expected at least one UN member that drives on the left")
	}
	wantPresent := map[string]bool{"United Kingdom": false, "Japan": false, "India": false}
	for _, n := range leftUNNames {
		if _, ok := wantPresent[n]; ok {
			wantPresent[n] = true
		}
	}
	for name, present := range wantPresent {
		if !present {
			t.Fatalf("expected %q in the left-driving UN-member list, got %v", name, leftUNNames)
		}
	}

	// Spot checks.
	spot := map[string]string{"GB": "left", "US": "right", "JP": "left", "TH": "left", "IN": "left"}
	for iso, want := range spot {
		c, found, err := store.GetCountryByISO(ctx, iso)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", iso, found, err)
		}
		got := factSideByCountry[c.ID]
		if got != want {
			t.Fatalf("country %s drives_on = %q, want %q", iso, got, want)
		}
	}
}

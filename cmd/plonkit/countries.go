package main

import (
	"fmt"
	"sort"
	"strings"
)

// isoToSlug maps ISO 3166-1 alpha-2 codes to plonkit.net guide slugs. Every
// entry was cross-checked directly against the live sitemap's discovered
// "Guide Pages" block on 2026-07-18 (see discoverGuideIndex) — this is not a
// blind slugify(name) guess, it is the real, observed slug for that code.
//
// Deliberately NOT covered by this prototype table (documented, not silently
// dropped — see vibe/plonkit-topics.md "Limitations"):
//   - sub-national guides plonkit publishes with no ISO 3166-1 alpha-2 code
//     of their own: Alaska, Hawaii (US states), Azores, Madeira (Portuguese
//     archipelagos);
//   - "IL" is intentionally omitted: plonkit's only guide touching Israel is
//     the combined "israel-west-bank" slug, not an Israel-only guide, so
//     mapping IL -> that slug would misrepresent what the guide covers;
//   - the two non-country entries in the discovered guide index itself,
//     "beginners-guide" and "spillover-countries", have no ISO code and are
//     never targets of this map;
//   - roughly 60 additional UN member states plonkit does have guides for
//     but this prototype's table doesn't enumerate — the full pipeline needs
//     either a complete static ISO table or direct fuzzy-matching of country
//     names against the discovered slug list (see design doc open
//     questions).
var isoToSlug = map[string]string{
	"AD": "andorra",
	"AE": "united-arab-emirates",
	"AL": "albania",
	"AQ": "antarctica",
	"AR": "argentina",
	"AS": "american-samoa",
	"AT": "austria",
	"AU": "australia",
	"BD": "bangladesh",
	"BE": "belgium",
	"BG": "bulgaria",
	"BM": "bermuda",
	"BO": "bolivia",
	"BR": "brazil",
	"BT": "bhutan",
	"BW": "botswana",
	"BY": "belarus",
	"CA": "canada",
	"CC": "cocos-islands",
	"CH": "switzerland",
	"CL": "chile",
	"CN": "china",
	"CO": "colombia",
	"CR": "costa-rica",
	"CW": "curacao",
	"CX": "christmas-island",
	"CY": "cyprus",
	"CZ": "czechia",
	"DE": "germany",
	"DK": "denmark",
	"DO": "dominican-republic",
	"EC": "ecuador",
	"EE": "estonia",
	"EG": "egypt",
	"ES": "spain",
	"FI": "finland",
	"FK": "falkland-islands",
	"FO": "faroe-islands",
	"FR": "france",
	"GB": "united-kingdom",
	"GH": "ghana",
	"GI": "gibraltar",
	"GL": "greenland",
	"GS": "south-georgia-sandwich-islands",
	"GR": "greece",
	"GT": "guatemala",
	"GU": "guam",
	"HK": "hong-kong",
	"HR": "croatia",
	"HU": "hungary",
	"ID": "indonesia",
	"IE": "ireland",
	"IM": "isle-of-man",
	"IN": "india",
	"IO": "british-indian-ocean-territory",
	"IQ": "iraq",
	"IS": "iceland",
	"IT": "italy",
	"JE": "jersey",
	"JO": "jordan",
	"JP": "japan",
	"KE": "kenya",
	"KG": "kyrgyzstan",
	"KH": "cambodia",
	"KR": "south-korea",
	"KZ": "kazakhstan",
	"LA": "laos",
	"LB": "lebanon",
	"LI": "liechtenstein",
	"LK": "sri-lanka",
	"LS": "lesotho",
	"LT": "lithuania",
	"LU": "luxembourg",
	"LV": "latvia",
	"MC": "monaco",
	"ME": "montenegro",
	"MG": "madagascar",
	"MK": "north-macedonia",
	"ML": "mali",
	"MN": "mongolia",
	"MO": "macau",
	"MP": "northern-mariana-islands",
	"MQ": "martinique",
	"MT": "malta",
	"MX": "mexico",
	"MY": "malaysia",
	"NA": "namibia",
	"NG": "nigeria",
	"NL": "netherlands",
	"NO": "norway",
	"NP": "nepal",
	"NZ": "new-zealand",
	"OM": "oman",
	"PA": "panama",
	"PE": "peru",
	"PH": "philippines",
	"PK": "pakistan",
	"PL": "poland",
	"PM": "saint-pierre-and-miquelon",
	"PN": "pitcairn-islands",
	"PR": "puerto-rico",
	"PT": "portugal",
	"QA": "qatar",
	"RE": "reunion",
	"RO": "romania",
	"RS": "serbia",
	"RU": "russia",
	"RW": "rwanda",
	"SE": "sweden",
	"SG": "singapore",
	"SI": "slovenia",
	"SJ": "svalbard",
	"SK": "slovakia",
	"SM": "san-marino",
	"SN": "senegal",
	"ST": "sao-tome-and-principe",
	"SZ": "eswatini",
	"TH": "thailand",
	"TN": "tunisia",
	"TR": "turkey",
	"TW": "taiwan",
	"TZ": "tanzania",
	"UA": "ukraine",
	"UG": "uganda",
	"UM": "us-minor-outlying-islands",
	"US": "united-states",
	"UY": "uruguay",
	"VI": "us-virgin-islands",
	"VU": "vanuatu",
	"VN": "vietnam",
	"ZA": "south-africa",
}

// resolveCountry maps an ISO2 code (case-insensitive) to its plonkit slug,
// then confirms that slug is present in guideIndex (the set freshly
// discovered from sitemap.xml — see discoverGuideIndex) rather than trusting
// the embedded table blindly, so a site restructuring fails loudly instead
// of silently scraping a 404 or stale content.
func resolveCountry(iso string, guideIndex map[string]struct{}) (slug string, err error) {
	iso = strings.ToUpper(strings.TrimSpace(iso))
	slug, ok := isoToSlug[iso]
	if !ok {
		return "", fmt.Errorf("country code %q is not in this prototype's embedded ISO->slug table (cmd/plonkit/countries.go); known codes: %s", iso, strings.Join(knownCountryCodes(), ", "))
	}
	if _, ok := guideIndex[slug]; !ok {
		return "", fmt.Errorf("resolved slug %q for %q is not in the sitemap's currently discovered guide index — site structure may have changed", slug, iso)
	}
	return slug, nil
}

// knownCountryCodes returns the sorted list of ISO2 codes isoToSlug covers,
// for a helpful error message when the caller requests one that isn't.
func knownCountryCodes() []string {
	codes := make([]string, 0, len(isoToSlug))
	for k := range isoToSlug {
		codes = append(codes, k)
	}
	sort.Strings(codes)
	return codes
}

// Package countrytier is the shared "how well-known is this country"
// difficulty rubric used by topic packages whose item tier should track a
// country's real-world familiarity rather than any property of the quizzed
// fact itself (e.g. roadside's which-side-of-the-road quiz, tld's domain
// quiz). It exists so that rubric (architecture §4's country table) is
// defined exactly once instead of reimplemented per topic — it was first
// implemented unexported in internal/topics/roadside; this package is the
// one shared home for it.
package countrytier

// tier0ISO is the task-specified tier-0 set: universally-known countries.
var tier0ISO = map[string]bool{
	"US": true, "GB": true, "FR": true, "DE": true, "JP": true,
	"CA": true, "AU": true, "IT": true, "ES": true,
}

// tier1ISO is every G20 country member NOT already in tier0ISO (the task
// brief's "remaining G20 members" rule). The G20 itself also counts the EU
// as a member, but the EU is not a country, so it is excluded here.
var tier1ISO = map[string]bool{
	"AR": true, "BR": true, "CN": true, "IN": true, "ID": true,
	"MX": true, "RU": true, "SA": true, "ZA": true, "KR": true, "TR": true,
}

// Tier implements the shared country-familiarity tier rubric: checked in
// this order, first match wins.
//   - tier 0: the 9 universally-known countries in tier0ISO
//   - tier 1: every other G20 member, in tier1ISO
//   - tier 2: any other UN member with GeoGuessr coverage
//   - tier 3: any other UN member without GeoGuessr coverage
//   - tier 4: everything else — territories, dependencies, subdivisions
//     (!unMember), regardless of coverage
func Tier(iso2 string, unMember, ggCoverage bool) int16 {
	switch {
	case tier0ISO[iso2]:
		return 0
	case tier1ISO[iso2]:
		return 1
	case unMember && ggCoverage:
		return 2
	case unMember && !ggCoverage:
		return 3
	default:
		return 4
	}
}

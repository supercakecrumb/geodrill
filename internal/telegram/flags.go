package telegram

// answerFaceEntry is a compact button face for a language: a representative
// country flag plus a short country code.
type answerFaceEntry struct {
	Flag string
	Code string
}

// answerFace maps a deck language (ISO-639-3 key) to its answerFaceEntry.
//
// Every flag here is a country you can actually be dropped in in GeoGuessr
// (i.e. with official Google Street View), so the flag doubles as a "where
// would I click" hint. That drives a few deliberate picks over the obvious
// one:
//   - Chinese → Taiwan (🇹🇼), not mainland China: China has no usable official
//     Street View, so Chinese text in GeoGuessr means Taiwan (also HK/Macau/SG).
//   - Catalan → Andorra (🇦🇩): its iconic GeoGuessr home (Catalonia is Spain).
var answerFace = map[string]answerFaceEntry{
	// Romance
	"spa": {Flag: "🇪🇸", Code: "ES"},
	"por": {Flag: "🇵🇹", Code: "PT"},
	"ita": {Flag: "🇮🇹", Code: "IT"},
	"fra": {Flag: "🇫🇷", Code: "FR"},
	"cat": {Flag: "🇦🇩", Code: "AD"},
	"ron": {Flag: "🇷🇴", Code: "RO"},
	// CJK — Chinese maps to Taiwan (mainland China isn't playable).
	"cmn": {Flag: "🇹🇼", Code: "TW"},
	"jpn": {Flag: "🇯🇵", Code: "JP"},
	"kor": {Flag: "🇰🇷", Code: "KR"},
	// Slavic (Latin)
	"pol": {Flag: "🇵🇱", Code: "PL"},
	"ces": {Flag: "🇨🇿", Code: "CZ"},
	"slk": {Flag: "🇸🇰", Code: "SK"},
	"hrv": {Flag: "🇭🇷", Code: "HR"},
	"slv": {Flag: "🇸🇮", Code: "SI"},
	// Slavic (Cyrillic)
	"rus": {Flag: "🇷🇺", Code: "RU"},
	"ukr": {Flag: "🇺🇦", Code: "UA"},
	"bul": {Flag: "🇧🇬", Code: "BG"},
	"srp": {Flag: "🇷🇸", Code: "RS"},
	"mkd": {Flag: "🇲🇰", Code: "MK"},
	// Nordic
	"swe": {Flag: "🇸🇪", Code: "SE"},
	"nor": {Flag: "🇳🇴", Code: "NO"},
	"dan": {Flag: "🇩🇰", Code: "DK"},
	"isl": {Flag: "🇮🇸", Code: "IS"},
	"fin": {Flag: "🇫🇮", Code: "FI"},
	// Southeast Asia
	"tha": {Flag: "🇹🇭", Code: "TH"},
	"khm": {Flag: "🇰🇭", Code: "KH"},
	"lao": {Flag: "🇱🇦", Code: "LA"},
	"mya": {Flag: "🇲🇲", Code: "MM"},
	"vie": {Flag: "🇻🇳", Code: "VN"},
	"ind": {Flag: "🇮🇩", Code: "ID"},
	"msa": {Flag: "🇲🇾", Code: "MY"},
	// South Asian scripts — mostly India; the flag hints at the country you'd
	// guess, the language name/script is the actual answer.
	"hin": {Flag: "🇮🇳", Code: "IN"},
	"mar": {Flag: "🇮🇳", Code: "IN"},
	"ben": {Flag: "🇧🇩", Code: "BD"},
	"tam": {Flag: "🇮🇳", Code: "IN"},
	"tel": {Flag: "🇮🇳", Code: "IN"},
	"guj": {Flag: "🇮🇳", Code: "IN"},
	"kan": {Flag: "🇮🇳", Code: "IN"},
	"mal": {Flag: "🇮🇳", Code: "IN"},
	"pan": {Flag: "🇮🇳", Code: "IN"},
	"sin": {Flag: "🇱🇰", Code: "LK"},
}

// answerLabel renders a language key as a button label per the user's chosen
// style:
//   - "code": flag + country code (e.g. "🇵🇹 PT"), falling back to fullName
//     when the key has no mapped face.
//   - "plain": fullName, never a flag.
//   - "name" (default, or any unrecognized style): flag + fullName, falling
//     back to fullName alone when the key has no mapped face.
func answerLabel(style, key, fullName string) string {
	face, ok := answerFace[key]

	switch style {
	case "code":
		if ok {
			return face.Flag + " " + face.Code
		}
		return fullName
	case "plain":
		return fullName
	default: // "name" and anything else
		if ok {
			return face.Flag + " " + fullName
		}
		return fullName
	}
}

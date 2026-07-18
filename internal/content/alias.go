package content

// downloadAliases maps a deck answer key (ISO-639-3) to the Tatoeba
// per-language export code that actually serves its sentences, for the cases
// where Tatoeba doesn't publish the macrolanguage code directly.
//
// Norwegian (nor) has no per-language export; Tatoeba splits it into Bokmål
// (nob) and Nynorsk (nno). We fetch Bokmål — the dominant written form and
// what GeoGuessr text almost always shows — while keeping the deck answer key
// nor ("Norwegian") so the button and stats read naturally.
var downloadAliases = map[string]string{
	"nor": "nob", // Norwegian → Bokmål
	"msa": "zsm", // Malay (macro) → Standard Malay, which is what Tatoeba exports
}

// DownloadCode returns the Tatoeba export code to download and match in the
// TSV for a deck answer key: the alias when one exists, else the key
// unchanged. Content is still stored under the original answer key.
func DownloadCode(key string) string {
	if dl, ok := downloadAliases[key]; ok {
		return dl
	}
	return key
}

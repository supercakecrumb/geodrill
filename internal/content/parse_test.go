package content

import "testing"

func TestParseLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantID   string
		wantLang string
		wantText string
		wantOK   bool
	}{
		{
			name:     "valid",
			line:     "1276\tspa\tLa gente se olvidará de nosotros.",
			wantID:   "1276",
			wantLang: "spa",
			wantText: "La gente se olvidará de nosotros.",
			wantOK:   true,
		},
		{
			name:     "valid with trailing CRLF",
			line:     "42\trus\tПривет, как дела?\r\n",
			wantID:   "42",
			wantLang: "rus",
			wantText: "Привет, как дела?",
			wantOK:   true,
		},
		{
			name:     "text containing tabs is preserved via SplitN(3)",
			line:     "5\tjpn\tこんにちは\tなに",
			wantID:   "5",
			wantLang: "jpn",
			wantText: "こんにちは\tなに",
			wantOK:   true,
		},
		{
			name:   "malformed - only one field",
			line:   "no tabs here",
			wantOK: false,
		},
		{
			name:   "malformed - only two fields",
			line:   "1\tspa",
			wantOK: false,
		},
		{
			name:   "malformed - empty id",
			line:   "\tspa\ttext here",
			wantOK: false,
		},
		{
			name:   "malformed - empty lang",
			line:   "1\t\ttext here",
			wantOK: false,
		},
		{
			name:   "malformed - empty text",
			line:   "1\tspa\t",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, lang, text, ok := ParseLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ParseLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if lang != tt.wantLang {
				t.Errorf("lang = %q, want %q", lang, tt.wantLang)
			}
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
		})
	}
}

func TestLengthOK(t *testing.T) {
	// 19 latin runes - too short
	s19 := "1234567890123456789"
	// 20 latin runes - boundary, ok
	s20 := "12345678901234567890"
	// 120 latin runes - boundary, ok
	s120 := repeatRune('a', 120)
	// 121 latin runes - too long
	s121 := repeatRune('a', 121)
	// multibyte CJK string, exactly 20 runes (each rune is 3 bytes in UTF-8)
	cjk20 := repeatRune('日', 20)
	cjk19 := repeatRune('日', 19)

	tests := []struct {
		name string
		text string
		min  int
		max  int
		want bool
	}{
		{"19 runes below default min", s19, DefaultMinLen, DefaultMaxLen, false},
		{"20 runes at default min boundary", s20, DefaultMinLen, DefaultMaxLen, true},
		{"120 runes at default max boundary", s120, DefaultMinLen, DefaultMaxLen, true},
		{"121 runes above default max", s121, DefaultMinLen, DefaultMaxLen, false},
		{"cjk 20 runes multibyte at min boundary", cjk20, DefaultMinLen, DefaultMaxLen, true},
		{"cjk 19 runes multibyte below min", cjk19, DefaultMinLen, DefaultMaxLen, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LengthOK(tt.text, tt.min, tt.max); got != tt.want {
				t.Errorf("LengthOK(len=%d runes) = %v, want %v", len([]rune(tt.text)), got, tt.want)
			}
		})
	}
}

func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

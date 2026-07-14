package content

import "testing"

func TestScriptOK(t *testing.T) {
	tests := []struct {
		name string
		lang string
		text string
		want bool
	}{
		{
			name: "srp in Cyrillic passes",
			lang: "srp",
			text: "Добро јутро, како си данас?",
			want: true,
		},
		{
			name: "srp in Latin letters is rejected",
			lang: "srp",
			text: "Dobro jutro, kako si danas?",
			want: false,
		},
		{
			name: "jpn mixing Han and kana passes",
			lang: "jpn",
			text: "私は日本語を勉強しています。",
			want: true,
		},
		{
			name: "pure Latin string rejected for rus",
			lang: "rus",
			text: "This is a plain English sentence.",
			want: false,
		},
		{
			name: "digits and punctuation only are exempt, spa",
			lang: "spa",
			text: "123 456, 789 - 000!",
			want: true,
		},
		{
			name: "digits and punctuation only are exempt, rus",
			lang: "rus",
			text: "42 - 100% (2024)",
			want: true,
		},
		{
			name: "unknown lang always false",
			lang: "xyz",
			text: "Anything at all.",
			want: false,
		},
		{
			name: "cmn Han passes",
			lang: "cmn",
			text: "我今天很高兴。",
			want: true,
		},
		{
			name: "kor Hangul passes",
			lang: "kor",
			text: "오늘 날씨가 좋습니다.",
			want: true,
		},
		{
			name: "tha Thai passes",
			lang: "tha",
			text: "วันนี้อากาศดีมาก",
			want: true,
		},
		{
			name: "spa Latin with accented letters passes",
			lang: "spa",
			text: "La gente se olvidará de nosotros mañana.",
			want: true,
		},
		{
			name: "spa mixed with Cyrillic letter fails",
			lang: "spa",
			text: "Hola мир",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScriptOK(tt.lang, tt.text); got != tt.want {
				t.Errorf("ScriptOK(%q, %q) = %v, want %v", tt.lang, tt.text, got, tt.want)
			}
		})
	}
}

func TestExpectedScripts(t *testing.T) {
	if _, ok := ExpectedScripts("xyz"); ok {
		t.Error("ExpectedScripts(\"xyz\") ok = true, want false for unknown lang")
	}
	scripts, ok := ExpectedScripts("jpn")
	if !ok {
		t.Fatal("ExpectedScripts(\"jpn\") ok = false, want true")
	}
	if len(scripts) != 3 {
		t.Errorf("ExpectedScripts(\"jpn\") returned %d scripts, want 3 (Han, Hiragana, Katakana)", len(scripts))
	}
}

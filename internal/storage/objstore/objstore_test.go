package objstore

import "testing"

func TestParseGarageRef(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantBucket string
		wantKey    string
		wantOK     bool
	}{
		{
			name:       "city map ref",
			in:         "garage://apps-geodrill/citymaps/paris.png",
			wantBucket: "apps-geodrill",
			wantKey:    "citymaps/paris.png",
			wantOK:     true,
		},
		{
			name:       "key with nested slashes",
			in:         "garage://bucket/a/b/c.png",
			wantBucket: "bucket",
			wantKey:    "a/b/c.png",
			wantOK:     true,
		},
		{
			name:       "single-segment key",
			in:         "garage://bucket/file.png",
			wantBucket: "bucket",
			wantKey:    "file.png",
			wantOK:     true,
		},
		{name: "bare disk path (flags)", in: "data/flags/fr.png", wantOK: false},
		{name: "empty string", in: "", wantOK: false},
		{name: "scheme only", in: "garage://", wantOK: false},
		{name: "bucket only, no key", in: "garage://bucket", wantOK: false},
		{name: "bucket with trailing slash, empty key", in: "garage://bucket/", wantOK: false},
		{name: "leading slash, empty bucket", in: "garage:///key.png", wantOK: false},
		{name: "wrong scheme", in: "s3://bucket/key.png", wantOK: false},
		{name: "http url", in: "http://garage:3900/bucket/key", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bucket, key, ok := ParseGarageRef(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ParseGarageRef(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if bucket != tc.wantBucket || key != tc.wantKey {
				t.Fatalf("ParseGarageRef(%q) = (%q, %q), want (%q, %q)",
					tc.in, bucket, key, tc.wantBucket, tc.wantKey)
			}
		})
	}
}

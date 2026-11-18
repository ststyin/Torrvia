package model

import "testing"

func TestSameResourceUsesMultilingualReleaseNames(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"겨울.2024.1080p", "봄날.2024.1080p", false},
		{"الليل.2024.1080p", "النهار.2024.1080p", false},
		{"Movie S01E03.mkv", "MOVIE.S01E03", true},
		{"?", "!", false},
	} {
		a, b := Torrent{Title: tc.a, SizeBytes: 1000}, Torrent{Title: tc.b, SizeBytes: 1000}
		if a.SameResource(&b) != tc.same || b.SameResource(&a) != tc.same {
			t.Errorf("unexpected match for %q and %q", tc.a, tc.b)
		}
	}
}

package store

import (
	"os"
	"slices"
	"testing"
)

func TestRecentAfterWrap(t *testing.T) {
	h, err := NewHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path, []byte("bad line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		if err := h.Append(Record{Query: q}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		limit int
		want  []string
	}{{3, []string{"7", "6", "5"}}, {1, []string{"7"}}, {9, []string{"7", "6", "5", "4", "3", "2", "1"}}, {0, []string{"7", "6", "5", "4", "3", "2", "1"}}} {
		recs, err := h.Recent(tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range recs {
			got = append(got, r.Query)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Recent(%d) = %v", tc.limit, got)
		}
	}
}

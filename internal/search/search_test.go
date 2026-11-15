package search

import (
	"sync"
	"testing"
	"time"

	"github.com/ststyin/torrvia/internal/model"
)

func TestCacheConcurrentReset(t *testing.T) {
	c := newResultCache(time.Minute)
	q := model.Query{Keyword: "Inception"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, operation := range []func(){
		func() { c.get(q) },
		func() { c.put(q, nil) },
		func() { c.reset(time.Minute) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 1000; i++ {
				operation()
			}
		}()
	}
	close(start)
	wg.Wait()
}

func torrent(site, title string, seeders int, promo model.Promo, gb float64) model.Torrent {
	t := model.Torrent{
		ID:        site + ":" + title,
		Site:      site,
		Title:     title,
		Seeders:   seeders,
		Promo:     promo,
		SizeBytes: int64(gb * 1024 * 1024 * 1024),
	}
	t.Recompute()
	return t
}

func TestRankOrdersByPromoWeightedSeeders(t *testing.T) {
	items := []model.Torrent{
		torrent("a", "Inception.2010.1080p", 100, model.PromoNone, 12),
		torrent("b", "Inception.2010.2160p", 40, model.PromoFree, 60),
	}

	got := Rank(items, model.Query{})

	if len(got) != 2 {
		t.Fatalf("expected 2 results; got %d", len(got))
	}
	// 40 free seeders score 120; 100 ordinary seeders score 100.
	if got[0].Site != "b" {
		t.Errorf("expected the free resource first; got %s (scores %.1f vs %.1f)",
			got[0].Site, got[0].Score, got[1].Score)
	}
}

func TestRankMergesSameResourceAcrossSites(t *testing.T) {
	items := []model.Torrent{
		torrent("a", "Inception.2010.1080p.BluRay.x264", 100, model.PromoFree, 12.0),
		torrent("b", "Inception.2010.1080p.BluRay.x264", 30, model.PromoNone, 12.2),
		torrent("c", "Inception.2010.2160p.BluRay.x264", 80, model.PromoNone, 60.0),
	}

	got := Rank(items, model.Query{})

	// The first two releases share a title and differ in size by 1.7%; the third release stays separate.
	if len(got) != 2 {
		t.Fatalf("expected 2 deduplicated results; got %d", len(got))
	}
	merged := got[0]
	if merged.Site != "a" {
		t.Errorf("expected higher-scoring site a; got %s", merged.Site)
	}
	if len(merged.AlsoOn) != 1 {
		t.Errorf("expected 1 duplicate site; got %d", len(merged.AlsoOn))
	}
}

func TestRankFiltersFreeOnly(t *testing.T) {
	items := []model.Torrent{
		torrent("a", "Dune.2021.1080p", 200, model.PromoNone, 15),
		torrent("b", "Dune.2021.2160p", 10, model.PromoFree, 60),
	}

	got := Rank(items, model.Query{FreeOnly: true})

	if len(got) != 1 {
		t.Fatalf("expected 1 free result; got %d", len(got))
	}
	if got[0].Promo != model.PromoFree {
		t.Errorf("expected a free resource; got %s", got[0].Promo)
	}
}

func TestRankRespectsLimit(t *testing.T) {
	items := []model.Torrent{
		torrent("a", "A.2020.1080p", 10, model.PromoNone, 10),
		torrent("b", "B.2020.1080p", 20, model.PromoNone, 10),
		torrent("c", "C.2020.1080p", 30, model.PromoNone, 10),
	}
	// Rank returns the full ranking; the output boundary applies the limit.
	got := applyLimit(Rank(items, model.Query{}), 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 results; got %d", len(got))
	}
	if got[0].Title != "C.2020.1080p" {
		t.Errorf("expected the most-seeded result first; got %s", got[0].Title)
	}
}

func TestRankKeepsFullListForCache(t *testing.T) {
	items := make([]model.Torrent, 0, 60)
	for i := 0; i < 60; i++ {
		items = append(items, torrent("a", "T"+string(rune('A'+i%26))+".2020."+string(rune('0'+i%10)), i+1, model.PromoNone, float64(i+1)))
	}
	// The cache must receive the complete ranking even when no limit is specified.
	if got := Rank(items, model.Query{}); len(got) != 60 {
		t.Fatalf("expected 60 results; got %d", len(got))
	}
	if got := applyLimit(items, 0); len(got) != 50 {
		t.Fatalf("zero limit should default to 50 results; got %d", len(got))
	}
}

func TestRankDoesNotMergeDifferentSizes(t *testing.T) {
	items := []model.Torrent{
		torrent("a", "Dune.2021.1080p", 50, model.PromoNone, 10),
		torrent("b", "Dune.2021.1080p", 50, model.PromoNone, 40), // Same title with a substantially different size
	}
	got := Rank(items, model.Query{})
	if len(got) != 2 {
		t.Fatalf("different-sized releases must not merge; expected 2 results, got %d", len(got))
	}
}

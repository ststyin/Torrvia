// Package search aggregates concurrent tracker searches, ranks and deduplicates results, and supports both complete and streamed responses.
package search

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/model"
)

// Event is a streamed search message.
type Event struct {
	Type      string          `json:"type"` // site | done | error
	Site      string          `json:"site,omitempty"`
	SiteName  string          `json:"site_name,omitempty"`
	Items     []model.Torrent `json:"items,omitempty"`
	Error     string          `json:"error,omitempty"`
	Total     int             `json:"total,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms,omitempty"`
}

// SiteState records the most recent site health result.
type SiteState struct {
	OK      bool
	Message string
	At      time.Time
}

// Service aggregates site searches.
type Service struct {
	mu       sync.RWMutex
	adapters map[string]adapter.Adapter
	order    []string
	state    map[string]SiteState
	seen     map[string]model.Torrent
	cache    *resultCache
}

// New preserves configured site order without affecting final rankings.
func New(adapters []adapter.Adapter, cacheTTL time.Duration) *Service {
	s := &Service{
		adapters: make(map[string]adapter.Adapter, len(adapters)),
		order:    make([]string, 0, len(adapters)),
		state:    make(map[string]SiteState, len(adapters)),
		seen:     make(map[string]model.Torrent, 1024),
		cache:    newResultCache(cacheTTL),
	}
	for _, a := range adapters {
		s.adapters[a.ID()] = a
		s.order = append(s.order, a.ID())
	}
	return s
}

// ReplaceAdapters updates sites and clears cached results and health states after a configuration change.
func (s *Service) ReplaceAdapters(adapters []adapter.Adapter, cacheTTL time.Duration) {
	m := make(map[string]adapter.Adapter, len(adapters))
	order := make([]string, 0, len(adapters))
	for _, a := range adapters {
		m[a.ID()] = a
		order = append(order, a.ID())
	}

	s.mu.Lock()
	s.adapters = m
	s.order = order
	s.state = make(map[string]SiteState, len(adapters))
	s.mu.Unlock()

	s.cache.reset(cacheTTL)
}

// Adapters returns enabled adapters in configuration order.
func (s *Service) Adapters() []adapter.Adapter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]adapter.Adapter, 0, len(s.order))
	for _, id := range s.order {
		if a, ok := s.adapters[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

// Status returns a snapshot of site health states.
func (s *Service) Status() map[string]SiteState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]SiteState, len(s.state))
	for k, v := range s.state {
		out[k] = v
	}
	return out
}

func (s *Service) markState(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state[id] = SiteState{OK: false, Message: err.Error(), At: time.Now()}
		return
	}
	s.state[id] = SiteState{OK: true, At: time.Now()}
}

// remember keeps a bounded set of resources for subsequent detail and download requests.
func (s *Service) remember(items []model.Torrent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) > 4096 {
		s.seen = make(map[string]model.Torrent, 1024)
	}
	for _, t := range items {
		s.seen[t.ID] = t
	}
}

// Get looks up a resource from a recent search.
func (s *Service) Get(id string) (model.Torrent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.seen[id]
	return t, ok
}

// Adapter looks up a site adapter by ID.
func (s *Service) Adapter(siteID string) (adapter.Adapter, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.adapters[siteID]
	return a, ok
}

// targets selects the sites included in this query.
func (s *Service) targets(q model.Query) []adapter.Adapter {
	all := s.Adapters()
	if len(q.Sites) == 0 {
		return all
	}
	want := make(map[string]bool, len(q.Sites))
	for _, id := range q.Sites {
		want[id] = true
	}
	out := make([]adapter.Adapter, 0, len(q.Sites))
	for _, a := range all {
		if want[a.ID()] {
			out = append(out, a)
		}
	}
	return out
}

type siteResult struct {
	adapter adapter.Adapter
	items   []model.Torrent
	err     error
	elapsed time.Duration
}

// Stream searches concurrently and closes the channel after all sites return.
func (s *Service) Stream(ctx context.Context, q model.Query) <-chan Event {
	out := make(chan Event, 16)
	go func() {
		defer close(out)

		if hit, ok := s.cache.get(q); ok {
			limited := applyLimit(hit, q.Limit)
			out <- Event{Type: "done", Items: limited, Total: len(limited)}
			return
		}

		targets := s.targets(q)
		if len(targets) == 0 {
			out <- Event{Type: "error", Error: "No sites available; check the configuration"}
			return
		}

		results := make(chan siteResult, len(targets))
		start := time.Now()

		var wg sync.WaitGroup
		for _, a := range targets {
			wg.Add(1)
			go func(a adapter.Adapter) {
				defer wg.Done()
				t0 := time.Now()
				items, err := a.Search(ctx, q)
				s.markState(a.ID(), err)
				results <- siteResult{adapter: a, items: items, err: err, elapsed: time.Since(t0)}
			}(a)
		}
		go func() { wg.Wait(); close(results) }()

		var all []model.Torrent
		for r := range results {
			if r.err != nil {
				out <- Event{
					Type:     "error",
					Site:     r.adapter.ID(),
					SiteName: r.adapter.Name(),
					Error:    r.err.Error(),
				}
				continue
			}
			// Sort each site's preview before sending it to the Web UI.
			sortItems(r.items)
			s.remember(r.items)
			out <- Event{
				Type:      "site",
				Site:      r.adapter.ID(),
				SiteName:  r.adapter.Name(),
				Items:     r.items,
				ElapsedMS: r.elapsed.Milliseconds(),
			}
			all = append(all, r.items...)
		}

		ranked := Rank(all, q)
		// Cache the complete ranking; apply each request's limit only at the output boundary.
		s.cache.put(q, ranked)
		limited := applyLimit(ranked, q.Limit)
		out <- Event{
			Type:      "done",
			Items:     limited,
			Total:     len(limited),
			ElapsedMS: time.Since(start).Milliseconds(),
		}
	}()
	return out
}

// Search waits for all sites and returns the final ranking.
func (s *Service) Search(ctx context.Context, q model.Query) ([]model.Torrent, map[string]string) {
	errs := map[string]string{}
	for ev := range s.Stream(ctx, q) {
		switch ev.Type {
		case "error":
			if ev.Site != "" {
				errs[ev.Site] = ev.Error
			}
		case "done":
			return ev.Items, errs
		}
	}
	return []model.Torrent{}, errs
}

// Rank filters, deduplicates, and sorts resources.
func Rank(items []model.Torrent, q model.Query) []model.Torrent {
	filtered := make([]model.Torrent, 0, len(items))
	for _, t := range items {
		if q.Match(&t) {
			filtered = append(filtered, t)
		}
	}
	sortItems(filtered)

	// Keep the highest-scoring copy and record duplicate site IDs in also_on.
	merged := make([]model.Torrent, 0, len(filtered))
	for i := 0; i < len(filtered); i++ {
		if filtered[i].ID == "" {
			continue
		}
		best := filtered[i]
		for j := i + 1; j < len(filtered); j++ {
			if filtered[j].ID == "" {
				continue
			}
			if best.SameResource(&filtered[j]) {
				best.AlsoOn = append(best.AlsoOn, filtered[j].ID)
				filtered[j].ID = "" // Mark as merged
			}
		}
		merged = append(merged, best)
	}

	// Leave the full ranking intact for caching; applyLimit truncates output.
	return merged
}

// applyLimit applies the requested result limit.
func applyLimit(items []model.Torrent, limit int) []model.Torrent {
	if limit <= 0 {
		limit = 50
	}
	if len(items) > limit {
		return items[:limit]
	}
	return items
}

func sortItems(items []model.Torrent) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Seeders > items[j].Seeders
	})
}

// HealthCheck checks all sites concurrently.
func (s *Service) HealthCheck(ctx context.Context) {
	var wg sync.WaitGroup
	for _, a := range s.Adapters() {
		wg.Add(1)
		go func(a adapter.Adapter) {
			defer wg.Done()
			err := a.Health(ctx)
			s.markState(a.ID(), err)
		}(a)
	}
	wg.Wait()
}

// Result cache.

type cacheEntry struct {
	items   []model.Torrent
	expires time.Time
}

type resultCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cacheEntry
}

func newResultCache(ttl time.Duration) *resultCache {
	return &resultCache{ttl: ttl, m: make(map[string]cacheEntry)}
}

// reset clears cached results and updates the TTL after configuration changes.
func (c *resultCache) reset(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
	c.m = make(map[string]cacheEntry)
}

func cacheKey(q model.Query) string {
	q.Limit = 0
	b, _ := json.Marshal(q)
	return string(b)
}

func (c *resultCache) get(q model.Query) ([]model.Torrent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return nil, false
	}
	e, ok := c.m[cacheKey(q)]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.items, true
}

func (c *resultCache) put(q model.Query, items []model.Torrent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return
	}
	// Bound the cache to prevent unbounded growth.
	if len(c.m) > 512 {
		c.m = make(map[string]cacheEntry)
	}
	c.m[cacheKey(q)] = cacheEntry{items: items, expires: time.Now().Add(c.ttl)}
}

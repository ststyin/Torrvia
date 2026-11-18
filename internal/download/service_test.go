package download

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
)

// Signal when the second caller starts waiting, so the test does not depend on sleeps.
type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestConcurrentCrossSiteSubmission(t *testing.T) {
	var sources []adapter.Adapter
	for _, id := range []string{"one", "two"} {
		a, err := adapter.Build(config.Site{Type: "mock", ID: id, Name: id})
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, a)
	}
	catalog := search.New(sources, 0)
	items, _ := catalog.Search(t.Context(), model.Query{Keyword: "Interstellar"})
	if len(items) == 0 || len(items[0].AlsoOn) == 0 {
		t.Fatal("expected two sources for the same release")
	}

	started, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var adds atomic.Int32
	qbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/info":
			json.NewEncoder(w).Encode([]qb.Torrent{})
		case "/api/v2/torrents/add":
			if adds.Add(1) == 1 {
				close(started)
				<-resume
			}
			fmt.Fprint(w, "Ok.")
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbsrv.Close()
	defer once.Do(func() { close(resume) })
	c := qb.New(qbsrv.URL, "", "")
	d := New(catalog, func() (*qb.Client, string) { return c, "" })
	type outcome struct {
		out Result
		err error
	}
	calls := make(chan outcome, 2)
	send := func(ctx context.Context, id string) { out, err := d.Submit(ctx, id, ""); calls <- outcome{out, err} }
	go send(t.Context(), items[0].ID)
	select {
	case <-started:
	case result := <-calls:
		t.Fatalf("first call failed early: %+v", result)
	}
	waiter := &waitingContext{Context: t.Context(), waiting: make(chan struct{})}
	go send(waiter, items[0].AlsoOn[0])
	select {
	case <-waiter.waiting:
	case result := <-calls:
		t.Fatalf("second call did not wait: %+v", result)
	}
	once.Do(func() { close(resume) })
	for range 2 {
		result := <-calls
		if result.err != nil || !result.out.OK || result.out.Via != "magnet" {
			t.Fatalf("unexpected submission result: %+v", result)
		}
	}
	if adds.Load() != 1 {
		t.Fatalf("duplicate submissions: %d", adds.Load())
	}
	// The coordinator tracks only active requests. If a task is removed, it can be added again.
	out, err := d.Submit(t.Context(), items[0].ID, "")
	if !out.OK || err != nil || adds.Load() != 2 {
		t.Fatalf("subsequent submission: %+v, %v, adds=%d", out, err, adds.Load())
	}
}

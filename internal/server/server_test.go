package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
)

func TestHistoryUnavailable(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.history(w, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 when history is unavailable; got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == "" {
		t.Fatal("expected an error explaining that history is unavailable")
	}
}

func TestDownloadsAPI(t *testing.T) {
	qbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
			fmt.Fprint(w, "Ok.")
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("filter") != "completed" {
			t.Errorf("unexpected downloader request: %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `[{"name":"Fights Break Sphere S05E219","state":"stalledUP","progress":1}]`)
	}))
	defer qbsrv.Close()
	client := qb.New(qbsrv.URL, "", "")
	cfg := &config.Config{}
	cfg.Server.Token = "test-token"
	handler := New(Deps{Cfg: func() *config.Config { return cfg }, QB: func() *qb.Client { return client }}).Handler()
	for _, tc := range []struct {
		query string
		token string
		code  int
	}{
		{"?filter=completed", "", http.StatusUnauthorized},
		{"?q=Fights+Break+Sphere&filter=completed&limit=1", "test-token", http.StatusOK},
		{"?filter=invalid", "test-token", http.StatusBadRequest},
		{"?offset=-1", "test-token", http.StatusBadRequest},
		{"?limit=not-a-number", "test-token", http.StatusBadRequest},
	} {
		t.Run(tc.query+tc.token, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/downloads"+tc.query, nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			handler.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("expected HTTP %d, got %d: %s", tc.code, w.Code, w.Body.String())
			}
			if tc.code == http.StatusOK {
				var out qb.ListResult
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Count != 1 || out.Items[0].Progress != 1 {
					t.Fatalf("unexpected response: %s, %v", w.Body.String(), err)
				}
			}
		})
	}
}

func TestDownloadsAPIUnavailable(t *testing.T) {
	s := &Server{qb: func() *qb.Client { return nil }}
	w := httptest.NewRecorder()
	s.downloads(w, httptest.NewRequest(http.MethodGet, "/api/downloads", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "error") {
		t.Fatalf("missing downloader must produce HTTP 503: %d %s", w.Code, w.Body.String())
	}
	qbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
			fmt.Fprint(w, "Ok.")
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer qbsrv.Close()
	client := qb.New(qbsrv.URL, "", "")
	s.qb = func() *qb.Client { return client }
	w = httptest.NewRecorder()
	s.downloads(w, httptest.NewRequest(http.MethodGet, "/api/downloads", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("failed downloader must produce HTTP 502: %d %s", w.Code, w.Body.String())
	}
}

func TestDownloadAPISkipsExistingTask(t *testing.T) {
	a, err := adapter.Build(config.Site{ID: "demo", Type: "mock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := search.New([]adapter.Adapter{a}, 0)
	items, _ := svc.Search(context.Background(), model.Query{Keyword: "Interstellar"})
	resource := items[0]
	qbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/info":
			json.NewEncoder(w).Encode([]qb.Torrent{{Name: resource.Title, TotalSize: resource.SizeBytes, Progress: 1, State: "stalledUP"}})
		default:
			t.Errorf("duplicate must not be submitted: %s", r.URL.Path)
		}
	}))
	defer qbsrv.Close()
	client := qb.New(qbsrv.URL, "", "")
	s := &Server{svc: svc, qb: func() *qb.Client { return client }, cfg: func() *config.Config { return &config.Config{} }}
	w := httptest.NewRecorder()
	s.download(w, httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(`{"id":"`+resource.ID+`"}`)))
	var out struct {
		OK            bool        `json:"ok"`
		AlreadyExists bool        `json:"already_exists"`
		Existing      *qb.Torrent `json:"existing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || !out.OK || !out.AlreadyExists || out.Existing == nil || out.Existing.Progress != 1 {
		t.Fatalf("unexpected duplicate response: %d %s, %v", w.Code, w.Body.String(), err)
	}
}

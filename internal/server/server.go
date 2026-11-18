// Package server exposes shared business logic through the Web UI, REST API, and MCP endpoint.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/download"
	"github.com/ststyin/torrvia/internal/model"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
	"github.com/ststyin/torrvia/internal/store"
)

// Server holds HTTP dependencies. Configuration and downloader accessors always read the current runtime state.
type Server struct {
	initialServer config.ServerConfig
	configMu      sync.Mutex
	downloadsSvc  *download.Service
	cfg           func() *config.Config
	svc           *search.Service
	hist          *store.History
	web           fs.FS
	mcp           http.Handler
	apply         func(*config.Config) error
}

// Deps contains the HTTP server dependencies.
type Deps struct {
	Downloads *download.Service
	Cfg       func() *config.Config
	Svc       *search.Service
	Hist      *store.History
	Web       fs.FS
	MCP       http.Handler
	Apply     func(*config.Config) error
}

// New constructs the HTTP server.
func New(d Deps) *Server {
	return &Server{
		initialServer: d.Cfg().Server,
		downloadsSvc:  d.Downloads,
		cfg:           d.Cfg,
		svc:           d.Svc,
		hist:          d.Hist,
		web:           d.Web,
		mcp:           d.MCP,
		apply:         d.Apply,
	}
}

// Handler returns the authenticated routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/sites", s.sites)
	mux.HandleFunc("GET /api/search", s.searchJSON)
	mux.HandleFunc("GET /api/search/stream", s.searchStream)
	mux.HandleFunc("GET /api/torrent/{id}", s.torrent)
	mux.HandleFunc("POST /api/download", s.download)
	mux.HandleFunc("GET /api/downloads", s.downloads)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("POST /api/config/token", s.rotateToken)

	if s.mcp != nil {
		mux.Handle("/mcp", s.mcp)
		mux.Handle("/mcp/", s.mcp)
	}

	return s.withAuth(mux)
}

// withAuth protects /api and /mcp. The public home page contains no private data.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api") && !strings.HasPrefix(p, "/mcp") {
			next.ServeHTTP(w, r)
			return
		}
		token := s.cfg().Server.Token
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if tokenFrom(r) != token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "A valid access token is required",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("token")
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.web, "web/index.html")
	if err != nil {
		http.Error(w, "Web assets are missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"sites":      len(s.svc.Adapters()),
		"downloader": s.downloadsSvc.Configured(),
		"version":    version,
	})
}

// version is supplied by the main package at startup.
var version = "dev"

// SetVersion supplies the build version.
func SetVersion(v string) { version = v }

func (s *Server) sites(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("check") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		s.svc.HealthCheck(ctx)
	}
	status := s.svc.Status()
	sites := s.cfg().Sites
	out := make([]model.SiteStatus, 0, len(sites))
	for _, site := range sites {
		st := model.SiteStatus{
			ID:      site.ID,
			Name:    site.Name,
			Type:    site.Type,
			Enabled: site.Enabled,
		}
		if v, ok := status[site.ID]; ok {
			st.Checked = true
			st.OK = v.OK
			st.LastError = v.Message
			st.CheckedAt = v.At.Format(time.RFC3339)
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": out})
}

func parseQuery(r *http.Request) model.Query {
	q := r.URL.Query()
	out := model.Query{
		Keyword:    strings.TrimSpace(q.Get("q")),
		Resolution: strings.TrimSpace(q.Get("resolution")),
		FreeOnly:   q.Get("free") == "1" || q.Get("free") == "true",
	}
	if v, err := strconv.Atoi(q.Get("year")); err == nil {
		out.Year = v
	}
	if v, err := strconv.Atoi(q.Get("min_seeders")); err == nil {
		out.MinSeeders = v
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		out.Limit = v
	}
	if raw := q.Get("sites"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				out.Sites = append(out.Sites, id)
			}
		}
	}
	return out
}

func (s *Server) searchJSON(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r)
	if q.Keyword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing search keyword q"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	start := time.Now()
	items, errs := s.svc.Search(ctx, q)
	elapsed := time.Since(start)

	s.recordHistory(q, len(items), elapsed)
	writeJSON(w, http.StatusOK, map[string]any{
		"query":       q,
		"total":       len(items),
		"items":       items,
		"site_errors": errs,
		"elapsed_ms":  elapsed.Milliseconds(),
	})
}

// searchStream sends each site's results over SSE as they arrive.
func (s *Server) searchStream(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r)
	if q.Keyword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing search keyword q"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Streaming responses are not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	start := time.Now()

	for ev := range s.svc.Stream(r.Context(), q) {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return
		}
		if err := enc.Encode(ev); err != nil {
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return
		}
		flusher.Flush()

		if ev.Type == "done" {
			s.recordHistory(q, ev.Total, time.Since(start))
		}
	}
}

func (s *Server) torrent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.svc.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Resource not found; it may be from an older search. Search again",
		})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type downloadRequest struct {
	ID       string `json:"id"`
	SavePath string `json:"save_path"`
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	if !s.downloadsSvc.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Downloader not configured. Set the qBittorrent URL and credentials in Settings",
		})
		return
	}

	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	out, err := s.downloadsSvc.Submit(r.Context(), req.ID, req.SavePath)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, download.ErrNotConfigured):
			status = http.StatusServiceUnavailable
		case errors.Is(err, download.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, download.ErrNoSource):
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := qb.ListOptions{Query: q.Get("q"), Filter: q.Get("filter"), Hash: q.Get("hash")}
	for key, dest := range map[string]*int{"limit": &opts.Limit, "offset": &opts.Offset} {
		if q.Has(key) {
			n, err := strconv.Atoi(q.Get(key))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": key + " must be an integer"})
				return
			}
			*dest = n
		}
	}
	out, err := s.downloadsSvc.List(r.Context(), opts)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, download.ErrNotConfigured) {
			code = http.StatusServiceUnavailable
		} else if errors.Is(err, qb.ErrInvalidOptions) {
			code = http.StatusBadRequest
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if s.hist == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Search history is unavailable"})
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 20
	}
	recs, err := s.hist.Recent(n)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": recs})
}

func (s *Server) recordHistory(q model.Query, total int, elapsed time.Duration) {
	if s.hist == nil {
		return
	}
	filters := make([]string, 0, 3)
	if q.FreeOnly {
		filters = append(filters, "Freeleech")
	}
	if q.Resolution != "" {
		filters = append(filters, q.Resolution)
	}
	if q.MinSeeders > 0 {
		filters = append(filters, "Seeders >= "+strconv.Itoa(q.MinSeeders))
	}
	_ = s.hist.Append(store.Record{
		At:      time.Now(),
		Query:   q.Keyword,
		Filters: strings.Join(filters, " "),
		Total:   total,
		Elapsed: elapsed.Milliseconds(),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

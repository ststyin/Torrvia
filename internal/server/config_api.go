package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ststyin/torrvia/internal/config"
)

// Configuration endpoints return credential presence and length, never cookie or password values. Empty input preserves a secret; a single hyphen clears it.

type serverView struct {
	Addr            string `json:"addr"`
	Token           string `json:"token"`
	DataDir         string `json:"data_dir"`
	CacheTTLSeconds int    `json:"cache_ttl_seconds"`
}

type downloaderView struct {
	Type        string `json:"type"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"password_set"`
	SavePath    string `json:"save_path"`
}

type siteView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"base_url"`
	SearchPath string `json:"search_path"`
	CookieSet  bool   `json:"cookie_set"`
	CookieHint string `json:"cookie_hint"`
	UserAgent  string `json:"user_agent"`
	TimeoutSec int    `json:"timeout_seconds"`
}

type configView struct {
	Path       string         `json:"path"`
	Server     serverView     `json:"server"`
	Downloader downloaderView `json:"downloader"`
	Sites      []siteView     `json:"sites"`
}

func newConfigView(c *config.Config) configView {
	v := configView{
		Path: c.Path(),
		Server: serverView{
			Addr:            c.Server.Addr,
			Token:           c.Server.Token,
			DataDir:         c.Server.DataDir,
			CacheTTLSeconds: c.Server.CacheTTLSeconds,
		},
		Downloader: downloaderView{
			Type:        c.Downloader.Type,
			URL:         c.Downloader.URL,
			Username:    c.Downloader.Username,
			PasswordSet: c.Downloader.Password != "",
			SavePath:    c.Downloader.SavePath,
		},
		Sites: make([]siteView, 0, len(c.Sites)),
	}
	for _, s := range c.Sites {
		v.Sites = append(v.Sites, siteView{
			ID:         s.ID,
			Name:       s.Name,
			Type:       s.Type,
			Enabled:    s.Enabled,
			BaseURL:    s.BaseURL,
			SearchPath: s.SearchPath,
			CookieSet:  s.Cookie != "",
			CookieHint: secretHint(s.Cookie),
			UserAgent:  s.UserAgent,
			TimeoutSec: s.TimeoutSec,
		})
	}
	return v
}

func secretHint(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf("Saved (%d characters)", len(s))
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, newConfigView(s.cfg()))
}

// Configuration updates.

type serverInput struct {
	Addr            string `json:"addr"`
	DataDir         string `json:"data_dir"`
	CacheTTLSeconds int    `json:"cache_ttl_seconds"`
}

type downloaderInput struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	SavePath string `json:"save_path"`
}

type siteInput struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"base_url"`
	SearchPath string `json:"search_path"`
	Cookie     string `json:"cookie"`
	UserAgent  string `json:"user_agent"`
	TimeoutSec int    `json:"timeout_seconds"`
}

type configInput struct {
	Server     serverInput     `json:"server"`
	Downloader downloaderInput `json:"downloader"`
	Sites      []siteInput     `json:"sites"`
}

// keepOrReplace preserves empty input and clears secrets on a single hyphen.
func keepOrReplace(in, old string) string {
	switch in {
	case "":
		return old
	case "-":
		return ""
	default:
		return in
	}
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	if s.apply == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Configuration updates are not enabled"})
		return
	}

	var in configInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}

	cur := s.cfg()
	next := &config.Config{
		Server: config.ServerConfig{
			// Rotate tokens through the dedicated endpoint to avoid accidental lockouts.
			Token:           cur.Server.Token,
			Addr:            firstNonEmpty(in.Server.Addr, cur.Server.Addr),
			DataDir:         firstNonEmpty(in.Server.DataDir, cur.Server.DataDir),
			CacheTTLSeconds: in.Server.CacheTTLSeconds,
		},
		Downloader: config.DownloaderConfig{
			Type:     in.Downloader.Type,
			URL:      in.Downloader.URL,
			Username: in.Downloader.Username,
			Password: keepOrReplace(in.Downloader.Password, cur.Downloader.Password),
			SavePath: in.Downloader.SavePath,
		},
	}

	for _, si := range in.Sites {
		site := config.Site{
			ID:         si.ID,
			Name:       si.Name,
			Type:       si.Type,
			Enabled:    si.Enabled,
			BaseURL:    si.BaseURL,
			SearchPath: si.SearchPath,
			UserAgent:  si.UserAgent,
			TimeoutSec: si.TimeoutSec,
		}
		// Preserve existing cookies unless a replacement is supplied.
		if old, ok := cur.FindSite(si.ID); ok {
			site.Cookie = keepOrReplace(si.Cookie, old.Cookie)
		} else if si.Cookie != "-" {
			site.Cookie = si.Cookie
		}
		next.Sites = append(next.Sites, site)
	}

	if err := s.apply(next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	applied := s.cfg()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"config":           newConfigView(applied),
		"restart_required": applied.Server.Addr != cur.Server.Addr,
	})
}

// rotateToken invalidates the old access token immediately; clients must update their configuration.
func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	if s.apply == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Configuration updates are not enabled"})
		return
	}
	tok, err := config.NewToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	cur := s.cfg()
	next := *cur
	next.Server.Token = tok
	if err := s.apply(&next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
}

func firstNonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

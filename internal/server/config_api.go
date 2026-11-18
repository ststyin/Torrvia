package server

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ststyin/torrvia/internal/config"
)

// Configuration endpoints return credential presence and length, never cookie or password values. Empty input preserves a secret; a single hyphen clears it.

type downloaderView struct {
	config.DownloaderConfig
	PasswordSet bool `json:"password_set"`
}

type siteView struct {
	config.Site
	CookieSet  bool   `json:"cookie_set"`
	CookieHint string `json:"cookie_hint"`
}

type configView struct {
	Path       string              `json:"path"`
	Server     config.ServerConfig `json:"server"`
	Downloader downloaderView      `json:"downloader"`
	Sites      []siteView          `json:"sites"`
}

func newConfigView(c *config.Config) configView {
	v := configView{
		Path:       c.Path(),
		Server:     c.Server,
		Downloader: downloaderView{DownloaderConfig: c.Downloader, PasswordSet: c.Downloader.Password != ""},
		Sites:      make([]siteView, 0, len(c.Sites)),
	}
	for _, s := range c.Sites {
		v.Sites = append(v.Sites, siteView{
			Site:       s,
			CookieSet:  s.Cookie != "",
			CookieHint: secretHint(s.Cookie),
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

type downloaderInput struct {
	config.DownloaderConfig
	Password string `json:"password"`
}

type siteInput struct {
	config.Site
	Cookie string `json:"cookie"`
}

type configInput struct {
	Server     config.ServerConfig `json:"server"`
	Downloader downloaderInput     `json:"downloader"`
	Sites      []siteInput         `json:"sites"`
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

	s.configMu.Lock()
	defer s.configMu.Unlock()
	cur := s.cfg()
	next := &config.Config{Server: in.Server, Downloader: in.Downloader.DownloaderConfig}
	// Only the dedicated rotation endpoint changes the token.
	next.Server.Token = cur.Server.Token
	next.Server.Addr = cmp.Or(in.Server.Addr, cur.Server.Addr)
	next.Server.DataDir = cmp.Or(in.Server.DataDir, cur.Server.DataDir)
	next.Downloader.Password = keepOrReplace(in.Downloader.Password, cur.Downloader.Password)

	for _, si := range in.Sites {
		site := si.Site
		old, _ := cur.FindSite(si.ID)
		site.Cookie = keepOrReplace(si.Cookie, old.Cookie)
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
		"restart_required": applied.Server.Addr != s.initialServer.Addr || applied.Server.DataDir != s.initialServer.DataDir,
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

	s.configMu.Lock()
	defer s.configMu.Unlock()
	cur := s.cfg()
	next := *cur
	next.Server.Token = tok
	if err := s.apply(&next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
}

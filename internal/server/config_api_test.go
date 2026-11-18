package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ststyin/torrvia/internal/config"
)

func TestSettingsRoundTrip(t *testing.T) {
	initial := config.ServerConfig{Addr: ":8080", DataDir: "data", Token: "access"}
	current := &config.Config{Server: initial, Sites: []config.Site{{ID: "site", Cookie: "private-cookie"}}, Downloader: config.DownloaderConfig{Password: "private-password"}}
	s := New(Deps{Cfg: func() *config.Config { return current }, Apply: func(next *config.Config) error { current = next; return nil }})
	for _, tc := range []struct {
		dir, password, cookie, wantPassword, wantCookie string
		restart                                         bool
	}{
		{"new-data", "", "", "private-password", "private-cookie", true},
		{"new-data", "private-change", "private-new", "private-change", "private-new", true},
		{"data", "-", "-", "", "", false},
	} {
		in := map[string]any{"server": map[string]string{"data_dir": tc.dir, "token": "ignored"}, "downloader": map[string]string{"password": tc.password}, "sites": []map[string]string{{"id": "site", "cookie": tc.cookie, "user_agent": "TestAgent"}}}
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.putConfig(w, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(data))))
		if w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
			t.Fatal("save failed or exposed a credential")
		}
		var response struct {
			Config  configView
			Restart bool `json:"restart_required"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Restart != tc.restart || response.Config.Sites[0].UserAgent != "TestAgent" || response.Config.Downloader.PasswordSet != (tc.wantPassword != "") || response.Config.Sites[0].CookieSet != (tc.wantCookie != "") {
			t.Fatal("incorrect restart status or public settings")
		}
		if current.Server.Token != initial.Token || current.Downloader.Password != tc.wantPassword || current.Sites[0].Cookie != tc.wantCookie {
			t.Fatal("save changed credential semantics")
		}
	}
}

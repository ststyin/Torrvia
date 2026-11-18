package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Saving and reloading must preserve every field so a Web settings update cannot silently change the configuration.
func TestRenderParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	want := &Config{
		Server: ServerConfig{
			Addr:            ":18080",
			Token:           "abc123",
			DataDir:         "./data",
			CacheTTLSeconds: 120,
		},
		Downloader: DownloaderConfig{
			Type:     "qbittorrent",
			URL:      "http://10.0.0.2:8080",
			Username: "admin",
			Password: "p@ss\"word\\with\\quotes\x07",
			SavePath: "/downloads",
		},
		Sites: []Site{
			{
				ID:         "site-a",
				Name:       "Tracker A",
				Type:       "nexusphp",
				Enabled:    true,
				BaseURL:    "https://a.example",
				SearchPath: "/torrents.php",
				Cookie:     `session=fixture"quoted; path=fixture\path`,
				UserAgent:  "Mozilla/5.0 (Macintosh)",
				TimeoutSec: 25,
			},
			{ID: "demo", Name: "Demo tracker", Type: "mock", Enabled: false, SearchPath: "/custom", TimeoutSec: 20},
		},
	}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if got.Server != want.Server {
		t.Errorf("server mismatch:\n got %+v\nwant %+v", got.Server, want.Server)
	}
	if got.Downloader != want.Downloader {
		t.Errorf("downloader mismatch:\n got %+v\nwant %+v", got.Downloader, want.Downloader)
	}
	if len(got.Sites) != len(want.Sites) {
		t.Fatalf("got %d sites; want %d", len(got.Sites), len(want.Sites))
	}
	for i := range want.Sites {
		if got.Sites[i] != want.Sites[i] {
			t.Errorf("site %d mismatch:\n got %+v\nwant %+v", i, got.Sites[i], want.Sites[i])
		}
	}
}

// Configuration files contain credentials and must use mode 600.
func TestSaveRestrictsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := &Config{Server: ServerConfig{Addr: ":8080", Token: "t", DataDir: "./data"}}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("got permissions %o; want 600", perm)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	base := func() *Config {
		return &Config{
			Server: ServerConfig{Addr: ":8080"},
			Sites:  []Site{{ID: "a", Name: "A", Type: "mock"}},
		}
	}

	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"empty address", func(c *Config) { c.Server.Addr = "" }, "addr"},
		{"address without port", func(c *Config) { c.Server.Addr = "8080" }, "server.addr"},
		{"duplicate ID", func(c *Config) {
			c.Sites = append(c.Sites, Site{ID: "a", Name: "B", Type: "mock"})
		}, "duplicate"},
		{"ID with spaces", func(c *Config) { c.Sites[0].ID = "my site" }, "must contain only"},
		{"unknown type", func(c *Config) { c.Sites[0].Type = "unit3d" }, "unsupported"},
		{"nexusphp without base_url", func(c *Config) {
			c.Sites[0] = Site{ID: "a", Name: "A", Type: "nexusphp"}
		}, "base_url"},
		{"downloader without URL", func(c *Config) {
			c.Downloader = DownloaderConfig{Type: "qbittorrent"}
		}, "downloader.url"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mut(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected an error; got success")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadCreatesDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != path || c.Server.Addr != ":8080" || c.Server.DataDir != "./data" || c.Server.CacheTTLSeconds != 300 {
		t.Fatalf("unexpected default configuration: %+v", c.Server)
	}
	if len(c.Server.Token) != 48 {
		t.Fatal("first startup must generate an access token")
	}
	if len(c.Sites) != 1 || c.Sites[0].Type != "mock" || !c.Sites[0].Enabled {
		t.Fatalf("first startup must provide an enabled demo tracker: %+v", c.Sites)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Server.Token != c.Server.Token {
		t.Fatal("reloading must preserve the access token")
	}
}

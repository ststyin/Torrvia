// Package config reads and writes config.toml. TORRVIA_CONFIG selects its path; all settings live in this file. A hand-written template preserves useful comments.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Site configures access to a private tracker.
type Site struct {
	ID         string `toml:"id" json:"id"`
	Name       string `toml:"name" json:"name"`
	Type       string `toml:"type" json:"type"` // nexusphp | mock
	Enabled    bool   `toml:"enabled" json:"enabled"`
	BaseURL    string `toml:"base_url" json:"base_url"`
	SearchPath string `toml:"search_path" json:"search_path"`
	Cookie     string `toml:"cookie" json:"-"`
	UserAgent  string `toml:"user_agent" json:"-"`
	TimeoutSec int    `toml:"timeout_seconds" json:"timeout_seconds"`
}

// ServerConfig contains service settings.
type ServerConfig struct {
	Addr            string `toml:"addr"`
	Token           string `toml:"token"`
	DataDir         string `toml:"data_dir"`
	CacheTTLSeconds int    `toml:"cache_ttl_seconds"`
}

// DownloaderConfig configures the download client.
type DownloaderConfig struct {
	Type     string `toml:"type"` // qbittorrent | empty
	URL      string `toml:"url"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	SavePath string `toml:"save_path"`
}

// Enabled reports whether a supported downloader is configured.
func (d DownloaderConfig) Enabled() bool {
	return d.URL != "" && (d.Type == "" || d.Type == "qbittorrent")
}

// Config contains all service configuration.
type Config struct {
	Server     ServerConfig     `toml:"server"`
	Downloader DownloaderConfig `toml:"downloader"`
	Sites      []Site           `toml:"sites"`

	path string // Configuration path; excluded from serialization
}

// Path returns the configuration file path.
func (c *Config) Path() string { return c.path }

// CacheTTL returns the search cache duration; zero disables caching.
func (c *Config) CacheTTL() time.Duration {
	return time.Duration(c.Server.CacheTTLSeconds) * time.Second
}

// EnabledSites returns enabled sites.
func (c *Config) EnabledSites() []Site {
	out := make([]Site, 0, len(c.Sites))
	for _, s := range c.Sites {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}

// FindSite looks up a site by ID.
func (c *Config) FindSite(id string) (Site, bool) {
	for _, s := range c.Sites {
		if s.ID == id {
			return s, true
		}
	}
	return Site{}, false
}

// DefaultPath returns TORRVIA_CONFIG or the default local path.
func DefaultPath() string {
	if v := strings.TrimSpace(os.Getenv("TORRVIA_CONFIG")); v != "" {
		return v
	}
	return "./config.toml"
}

// Load reads the configuration, creating defaults if the file is missing.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("Failed to read %s: %w", path, err)
		}
		return bootstrap(path)
	}

	var c Config
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // Reject unknown fields instead of silently ignoring typos
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("Failed to parse %s: %w", path, err)
	}
	c.path = path

	changed, err := c.normalize()
	if err != nil {
		return nil, err
	}
	if changed {
		// Persist generated tokens and normalized defaults.
		if err := c.Save(path); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// normalize fills defaults and reports whether anything changed.
func (c *Config) normalize() (bool, error) {
	changed := false

	if strings.TrimSpace(c.Server.Addr) == "" {
		c.Server.Addr = ":8080"
		changed = true
	}
	if strings.TrimSpace(c.Server.DataDir) == "" {
		c.Server.DataDir = "./data"
		changed = true
	}
	if c.Server.CacheTTLSeconds < 0 {
		c.Server.CacheTTLSeconds = 0
		changed = true
	}
	if strings.TrimSpace(c.Server.Token) == "" {
		tok, err := NewToken()
		if err != nil {
			return false, err
		}
		c.Server.Token = tok
		changed = true
	}
	if strings.TrimSpace(c.Downloader.Type) == "" && c.Downloader.URL != "" {
		c.Downloader.Type = "qbittorrent"
		changed = true
	}

	for i := range c.Sites {
		s := &c.Sites[i]
		if s.Type == "" {
			s.Type = "nexusphp"
			changed = true
		}
		// Only real trackers need a search path.
		if s.Type == "nexusphp" && s.SearchPath == "" {
			s.SearchPath = "/torrents.php"
			changed = true
		}
		if s.TimeoutSec <= 0 {
			s.TimeoutSec = 20
			changed = true
		}
	}
	return changed, nil
}

// Validate rejects configurations that cannot work. Use ./torrvia -check to check connectivity and credentials.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Addr) == "" {
		return errors.New("server.addr must not be empty")
	}
	if !strings.Contains(c.Server.Addr, ":") {
		return fmt.Errorf("server.addr must be :8080 or 0.0.0.0:8080; got %q", c.Server.Addr)
	}
	if c.Server.CacheTTLSeconds < 0 {
		return errors.New("server.cache_ttl_seconds must not be negative")
	}

	seen := make(map[string]bool, len(c.Sites))
	for i, s := range c.Sites {
		pos := fmt.Sprintf("site #%d", i+1)
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("%s is missing id", pos)
		}
		if !validID(s.ID) {
			return fmt.Errorf("site id %q must contain only lowercase letters, digits, underscores, and hyphens", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate site id %q", s.ID)
		}
		seen[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("site %s is missing name", s.ID)
		}
		switch s.Type {
		case "nexusphp":
			if strings.TrimSpace(s.BaseURL) == "" {
				return fmt.Errorf("nexusphp site %s requires base_url", s.ID)
			}
			if !strings.HasPrefix(s.BaseURL, "http://") && !strings.HasPrefix(s.BaseURL, "https://") {
				return fmt.Errorf("site %s base_url must start with http:// or https://", s.ID)
			}
		case "mock":
			// Demo sites need no additional fields.
		default:
			return fmt.Errorf("site %s has unsupported type %q; use nexusphp or mock", s.ID, s.Type)
		}
	}

	if d := c.Downloader; d.Type != "" {
		if d.Type != "qbittorrent" {
			return fmt.Errorf("downloader.type only supports qbittorrent; got %q", d.Type)
		}
		if strings.TrimSpace(d.URL) == "" {
			return errors.New("downloader.url is required when downloader.type is set")
		}
	}
	return nil
}

func validID(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return s != ""
}

// Save validates and writes the configuration atomically through a temporary file.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if path == "" {
		return errors.New("Configuration file path is unknown")
	}

	data := render(c)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("Failed to create configuration directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("Failed to create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("Failed to write configuration: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("Failed to sync configuration: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Restrict access to the cookies and passwords in this file.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("Failed to replace configuration file: %w", err)
	}
	c.path = path
	return nil
}

// NewToken generates an access token for initial setup and token rotation.
func NewToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("Failed to generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// q quotes strings for TOML using Go's compatible escaping rules for paths, cookies, and user agents.
func q(s string) string { return strconv.Quote(s) }

// render uses a template to preserve comments in the TOML configuration.
func render(c *Config) []byte {
	var b strings.Builder

	b.WriteString(`# Torrvia configuration
#
# Edit this file or use Settings in the Web UI; both update the same configuration.
# Changes apply immediately, except for the listen address, which requires a restart.
#
# This file contains tracker cookies and downloader credentials.
# Keep permissions at 600. The file is ignored by Git; never commit credentials.

`)

	b.WriteString("[server]\n")
	fmt.Fprintf(&b, "addr = %s\n", q(c.Server.Addr))
	b.WriteString("# All /api and /mcp requests require Authorization: Bearer <token>.\n")
	b.WriteString("# Leave blank to generate a token at startup. Update clients when changing it.\n")
	fmt.Fprintf(&b, "token = %s\n", q(c.Server.Token))
	fmt.Fprintf(&b, "data_dir = %s\n", q(c.Server.DataDir))
	b.WriteString("# Search cache lifetime in seconds. Set to 0 to disable caching.\n")
	fmt.Fprintf(&b, "cache_ttl_seconds = %d\n\n", c.Server.CacheTTLSeconds)

	b.WriteString(`# Downloader type: qbittorrent. Leave empty for search-only operation.
# The service retrieves torrent files with tracker cookies before passing them to the downloader.
# Tracker cookies stay on this machine.
[downloader]
`)
	fmt.Fprintf(&b, "type = %s\n", q(c.Downloader.Type))
	fmt.Fprintf(&b, "url = %s\n", q(c.Downloader.URL))
	fmt.Fprintf(&b, "username = %s\n", q(c.Downloader.Username))
	fmt.Fprintf(&b, "password = %s\n", q(c.Downloader.Password))
	b.WriteString("# Leave blank to use the downloader default directory.\n")
	fmt.Fprintf(&b, "save_path = %s\n", q(c.Downloader.SavePath))

	b.WriteString(`
# Sites.
#
# Supported site types:
#   nexusphp: private tracker template; requires base_url and cookie
#   mock: demo data for testing Web, REST, and MCP
#
# Log in to the site, open browser developer tools > Network, and copy the complete Cookie header.
# Enable Remember me if available; session-only logins also require PHPSESSID.
`)

	for _, s := range c.Sites {
		b.WriteString("\n[[sites]]\n")
		fmt.Fprintf(&b, "id = %s\n", q(s.ID))
		fmt.Fprintf(&b, "name = %s\n", q(s.Name))
		fmt.Fprintf(&b, "type = %s\n", q(s.Type))
		fmt.Fprintf(&b, "enabled = %t\n", s.Enabled)
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "base_url = %s\n", q(s.BaseURL))
		}
		if s.Type == "nexusphp" {
			fmt.Fprintf(&b, "search_path = %s\n", q(s.SearchPath))
		}
		if s.Cookie != "" {
			fmt.Fprintf(&b, "cookie = %s\n", q(s.Cookie))
		}
		if s.UserAgent != "" {
			fmt.Fprintf(&b, "user_agent = %s\n", q(s.UserAgent))
		}
		if s.TimeoutSec > 0 {
			fmt.Fprintf(&b, "timeout_seconds = %d\n", s.TimeoutSec)
		}
	}

	return []byte(b.String())
}

// bootstrap creates the default configuration when the file is missing.
func bootstrap(path string) (*Config, error) {
	c := &Config{
		Server: ServerConfig{CacheTTLSeconds: 300},
		Sites: []Site{{
			ID:      "demo",
			Name:    "Demo tracker",
			Type:    "mock",
			Enabled: true,
		}},
	}
	if _, err := c.normalize(); err != nil {
		return nil, err
	}
	if err := c.Save(path); err != nil {
		return nil, err
	}
	return c, nil
}

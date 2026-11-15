package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/qb"
)

// runCheck checks site connectivity, credentials, and the downloader without starting the service.
func runCheck(cfg *config.Config) int {
	fmt.Printf("\n  Config      %s\n", cfg.Path())
	fmt.Printf("  Data dir    %s\n", cfg.Server.DataDir)

	if len(cfg.Sites) == 0 {
		fmt.Println("\n  No sites configured. Add a site to the configuration:")
		fmt.Println(`
[[sites]]
id = "mysite"
name = "My tracker"
type = "nexusphp"
enabled = true
base_url = "https://example.org"
cookie = "uid=12345; pass=abcdef; ..."`)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	failures := 0
	fmt.Printf("\n  %d sites configured\n", len(cfg.Sites))

	for _, site := range cfg.Sites {
		fmt.Printf("\n  %s (%s)\n", site.ID, site.Name)
		fmt.Printf("      type=%s  enabled=%v\n", site.Type, site.Enabled)

		if site.BaseURL != "" {
			fmt.Printf("      base_url=%s  search_path=%s  timeout=%ds\n",
				site.BaseURL, site.SearchPath, site.TimeoutSec)
		}
		if site.Type == "nexusphp" {
			if site.Cookie == "" {
				fmt.Println("      Cookie  Missing; this site will be skipped")
				failures++
				continue
			}
			fmt.Printf("      Cookie  Set (%d characters)\n", len(site.Cookie))
		}

		if !site.Enabled {
			fmt.Println("      Status  Disabled; skipping check")
			continue
		}

		a, err := adapter.Build(site)
		if err != nil {
			fmt.Printf("      Status  Failed: %v\n", err)
			failures++
			continue
		}

		start := time.Now()
		if err := a.Health(ctx); err != nil {
			fmt.Printf("      Status  Failed: %v\n", err)
			fmt.Println("              Check cookie expiry, Remember me, proxy settings, and User-Agent")
			failures++
			continue
		}
		fmt.Printf("      Status  OK (%dms)\n", time.Since(start).Milliseconds())
	}

	fmt.Println()
	if cfg.Downloader.Enabled() {
		fmt.Printf("  Downloader  qBittorrent %s\n", cfg.Downloader.URL)
		if cfg.Downloader.Username == "" {
			fmt.Println("            Missing username (downloader.username)")
			failures++
		} else {
			start := time.Now()
			client := qb.New(cfg.Downloader.URL, cfg.Downloader.Username, cfg.Downloader.Password)
			if err := client.Ping(ctx); err != nil {
				fmt.Printf("            ✗ %v\n", err)
				failures++
			} else {
				fmt.Printf("            Login OK (%dms)\n", time.Since(start).Milliseconds())
				// Login and task submission use separate routes; verify both.
				start = time.Now()
				if err := client.Probe(ctx); err != nil {
					fmt.Printf("            Task submission unavailable: %v\n", err)
					failures++
				} else {
					fmt.Printf("            Task submission reachable (%dms)\n", time.Since(start).Milliseconds())
				}
				if cfg.Downloader.SavePath != "" {
					fmt.Printf("            Save path: %s\n", cfg.Downloader.SavePath)
				}
			}
		}
	} else {
		fmt.Println("  Downloader  Not configured. Open Settings in the Web UI,")
		fmt.Println("            or set url, username, and password in [downloader].")
	}

	fmt.Println()
	if failures > 0 {
		fmt.Printf("  %d checks failed\n\n", failures)
		return 1
	}
	fmt.Println("  All checks passed")
	return 0
}

// Command dump saves raw tracker HTML and prints parsed results for debugging site integrations. Example: go run ./cmd/dump -site mysite -q Interstellar -out /tmp/page.html
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
)

func main() {
	siteID := flag.String("site", "", "Site ID (see config.toml)")
	keyword := flag.String("q", "", "Search keyword")
	out := flag.String("out", "", "Save raw HTML to this path")
	fetchID := flag.String("fetch", "", "Fetch and validate the specified torrent ID")
	cfgPath := flag.String("config", "", "Configuration path (default ./config.toml)")
	flag.Parse()

	if *siteID == "" || (*keyword == "" && *fetchID == "") {
		flag.Usage()
		os.Exit(2)
	}

	path := *cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}

	site, ok := cfg.FindSite(*siteID)
	if !ok {
		log.Fatalf("Site %q not found", *siteID)
	}

	a, err := adapter.Build(site)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if d, ok := a.(interface {
		DumpPage(ctx context.Context, keyword string) ([]byte, error)
	}); ok && *out != "" {
		body, err := d.DumpPage(ctx, *keyword)
		if err != nil {
			log.Fatalf("Fetch failed: %v", err)
		}
		if err := os.WriteFile(*out, body, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Saved %d bytes to %s\n\n", len(body), *out)
	}

	if *fetchID != "" {
		f, ok := a.(adapter.TorrentFetcher)
		if !ok {
			log.Fatalf("Site %s does not support torrent retrieval", *siteID)
		}
		t := model.Torrent{
			ID:          *siteID + ":" + *fetchID,
			Site:        *siteID,
			DownloadURL: site.BaseURL + "/download.php?id=" + *fetchID,
		}
		data, name, err := f.FetchTorrent(ctx, t)
		if err != nil {
			log.Fatalf("Torrent retrieval failed: %v", err)
		}
		fmt.Printf("Torrent: %s  Size: %d bytes  First 4 bytes: %q\n", name, len(data), string(data[:min(len(data), 4)]))
		return
	}

	items, err := a.Search(ctx, model.Query{Keyword: *keyword})
	if err != nil {
		log.Fatalf("Search failed: %v", err)
	}

	fmt.Printf("Site %s returned %d resources\n\n", a.Name(), len(items))
	fmt.Printf("%-4s %-6s %-8s %-10s %-6s %s\n", "#", "Seed", "Leech", "Size", "Promo", "Title")
	for i, t := range items {
		fmt.Printf("%-4d %-6d %-8d %-10s %-6s %s\n",
			i+1, t.Seeders, t.Leechers, t.HumanSize(), t.Promo.Label(), truncate(t.Title, 56))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

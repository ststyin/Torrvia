// Package adapter provides a common tracker interface and keeps site-specific HTML parsing out of the application layers.
package adapter

import (
	"context"
	"fmt"

	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
)

// Adapter provides a site's search and connectivity operations.
type Adapter interface {
	ID() string
	Name() string
	Type() string
	// Search returns raw site results; the aggregation layer filters, ranks, and deduplicates them.
	Search(ctx context.Context, q model.Query) ([]model.Torrent, error)
	// Health checks connectivity and credential validity.
	Health(ctx context.Context) error
}

// TorrentFetcher optionally retrieves torrent files using site credentials before handing the bytes to the downloader.
type TorrentFetcher interface {
	FetchTorrent(ctx context.Context, t model.Torrent) (data []byte, filename string, err error)
}

// Build constructs an adapter and rejects unsupported or incomplete configurations.
func Build(site config.Site) (Adapter, error) {
	switch site.Type {
	case "mock":
		return newMock(site), nil
	case "nexusphp":
		if site.BaseURL == "" {
			return nil, fmt.Errorf("site %s is missing base_url", site.ID)
		}
		return newNexusPHP(site), nil
	default:
		return nil, fmt.Errorf("site %s has unsupported type %q", site.ID, site.Type)
	}
}

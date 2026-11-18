// Package mcpserver exposes tracker searches, download submissions, and live downloader tasks.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ststyin/torrvia/internal/download"
	"github.com/ststyin/torrvia/internal/model"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
)

// Deps contains MCP dependencies. Accessors pick up downloader changes without restarting the service.
type Deps struct {
	Downloads *download.Service
	Svc       *search.Service
}

// Handler serves Streamable HTTP at /mcp, keeping the MCP lifecycle tied to this service.
func Handler(d Deps) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "torrvia",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: "Torrvia searches media across private trackers on your local network. " +
			"Prefer an English title followed by the year for more reliable matches. " +
			"Results are ranked by seeders multiplied by promotion weight, favoring freeleech and double-upload promotions.",
	})

	registerSearch(s, d)
	registerGet(s, d)
	registerSites(s, d)
	registerDownloads(s, d)
	registerDownload(s, d)

	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

// ---------- search_torrents ----------

// SearchInput describes how the AI should construct a search query.
type SearchInput struct {
	Query      string   `json:"query" jsonschema:"Search keyword. Prefer an English title and year, for example Interstellar 2014; localized titles also work"`
	Year       int      `json:"year,omitempty" jsonschema:"Optional release year; only return titles containing this year to distinguish remakes"`
	Resolution string   `json:"resolution,omitempty" jsonschema:"Optional resolution filter: 2160p, 1080p, or 720p"`
	FreeOnly   bool     `json:"free_only,omitempty" jsonschema:"Return only freeleech or freeleech with double-upload credit"`
	MinSeeders int      `json:"min_seeders,omitempty" jsonschema:"Minimum seeders; 5 is recommended to avoid poorly seeded releases"`
	Sites      []string `json:"sites,omitempty" jsonschema:"Restrict site IDs; leave empty to search all configured sites"`
	Limit      int      `json:"limit,omitempty" jsonschema:"Maximum results; defaults to 20, with 30 or fewer recommended"`
}

// TorrentOut omits long download URLs and magnets from search responses to keep AI context small.
type TorrentOut struct {
	ID          string   `json:"id" jsonschema:"Resource ID for get_torrent or download_torrent"`
	Site        string   `json:"site" jsonschema:"Site ID"`
	SiteName    string   `json:"site_name" jsonschema:"Site name"`
	Title       string   `json:"title" jsonschema:"Original release name, typically including year, resolution, source, and release group"`
	SubTitle    string   `json:"subtitle,omitempty" jsonschema:"Site-provided localized title or description; use this to confirm the requested media"`
	Tags        []string `json:"tags,omitempty" jsonschema:"Site labels such as Chinese subtitles, Mandarin audio, or official release"`
	SizeGB      float64  `json:"size_gb" jsonschema:"Size in GB"`
	Seeders     int      `json:"seeders" jsonschema:"Seeders; more usually means faster downloads"`
	Promo       string   `json:"promo" jsonschema:"Promotion: free / 2xfree / 2x / 50% / 30% / none"`
	Resolution  string   `json:"resolution,omitempty" jsonschema:"Resolution"`
	PublishTime string   `json:"publish_time,omitempty" jsonschema:"Published at"`
	AlsoOn      []string `json:"also_on,omitempty" jsonschema:"IDs of the same resource on other sites"`
	DetailURL   string   `json:"detail_url,omitempty" jsonschema:"Site detail URL"`
}

// SearchOutput contains ranked search results.
type SearchOutput struct {
	Count      int               `json:"count" jsonschema:"Result count"`
	Items      []TorrentOut      `json:"items" jsonschema:"Resources sorted by ranking score"`
	SiteErrors map[string]string `json:"site_errors,omitempty" jsonschema:"Per-site errors; other sites remain available"`
	ElapsedMS  int64             `json:"elapsed_ms" jsonschema:"Elapsed time in milliseconds"`
}

func registerSearch(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_torrents",
		Description: "Search all configured private trackers concurrently, ranking results by seeders multiplied by promotion weight. " +
			"Individual site failures do not affect other results. For empty results, remove the year, shorten to the main title, then try another translation.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		in.Query = strings.TrimSpace(in.Query)
		if in.Query == "" {
			return nil, SearchOutput{}, errors.New("Missing search keyword")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		q := model.Query{
			Keyword:    in.Query,
			Year:       in.Year,
			Resolution: in.Resolution,
			FreeOnly:   in.FreeOnly,
			MinSeeders: in.MinSeeders,
			Sites:      in.Sites,
			Limit:      limit,
		}

		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		start := time.Now()
		items, errs := d.Svc.Search(ctx, q)
		out := SearchOutput{
			Count:      len(items),
			SiteErrors: errs,
			ElapsedMS:  time.Since(start).Milliseconds(),
			Items:      make([]TorrentOut, 0, len(items)),
		}
		for _, t := range items {
			out.Items = append(out.Items, toOut(t))
		}
		return nil, out, nil
	})
}

func toOut(t model.Torrent) TorrentOut {
	return TorrentOut{
		ID:          t.ID,
		Site:        t.Site,
		SiteName:    t.SiteName,
		Title:       t.Title,
		SubTitle:    t.SubTitle,
		Tags:        t.Tags,
		SizeGB:      float64(t.SizeBytes) / (1 << 30),
		Seeders:     t.Seeders,
		Promo:       string(t.Promo),
		Resolution:  t.Resolution,
		PublishTime: t.PublishTime,
		AlsoOn:      t.AlsoOn,
		DetailURL:   t.DetailURL,
	}
}

// ---------- get_torrent ----------

// GetInput identifies a resource by ID.
type GetInput struct {
	ID string `json:"id" jsonschema:"Resource ID returned by search_torrents"`
}

// GetOutput includes resource details and download links.
type GetOutput struct {
	ID          string   `json:"id"`
	Site        string   `json:"site"`
	Title       string   `json:"title"`
	SubTitle    string   `json:"subtitle,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	SizeGB      float64  `json:"size_gb"`
	Seeders     int      `json:"seeders"`
	Leechers    int      `json:"leechers"`
	Promo       string   `json:"promo"`
	PromoLabel  string   `json:"promo_label"`
	Resolution  string   `json:"resolution,omitempty"`
	PublishTime string   `json:"publish_time,omitempty"`
	DetailURL   string   `json:"detail_url,omitempty"`
	DownloadURL string   `json:"download_url,omitempty"`
	Magnet      string   `json:"magnet,omitempty"`
	AlsoOn      []string `json:"also_on,omitempty"`
}

func registerGet(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_torrent",
		Description: "Get full resource details by ID, including magnet and torrent download URLs. Use an ID returned by search_torrents.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, GetOutput, error) {
		t, ok := d.Svc.Get(in.ID)
		if !ok {
			return nil, GetOutput{}, errors.New("Resource not found; it may be from an older search. Run search_torrents again")
		}
		return nil, GetOutput{
			ID:          t.ID,
			Site:        t.Site,
			Title:       t.Title,
			SubTitle:    t.SubTitle,
			Tags:        t.Tags,
			SizeGB:      float64(t.SizeBytes) / (1 << 30),
			Seeders:     t.Seeders,
			Leechers:    t.Leechers,
			Promo:       string(t.Promo),
			PromoLabel:  t.Promo.Label(),
			Resolution:  t.Resolution,
			PublishTime: t.PublishTime,
			DetailURL:   t.DetailURL,
			DownloadURL: t.DownloadURL,
			Magnet:      t.Magnet,
			AlsoOn:      t.AlsoOn,
		}, nil
	})
}

// ---------- list_sites ----------

// SitesInput optionally requests a live connectivity check.
type SitesInput struct {
	Check bool `json:"check,omitempty" jsonschema:"When true, check site connectivity and cookie validity; this may take a few seconds"`
}

// SiteOut describes a site health result.
type SiteOut struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// SitesOutput lists configured sites.
type SitesOutput struct {
	Count int       `json:"count"`
	Sites []SiteOut `json:"sites"`
}

func registerSites(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_sites",
		Description: "List configured trackers and availability. When results are unexpectedly sparse, check for expired site cookies.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SitesInput) (*mcp.CallToolResult, SitesOutput, error) {
		if in.Check {
			c, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			d.Svc.HealthCheck(c)
		}
		status := d.Svc.Status()
		adapters := d.Svc.Adapters()
		out := SitesOutput{Sites: make([]SiteOut, 0, len(adapters))}
		for _, a := range adapters {
			so := SiteOut{ID: a.ID(), Name: a.Name(), Type: a.Type()}
			if v, ok := status[a.ID()]; ok {
				so.OK = v.OK
				so.Message = v.Message
				so.CheckedAt = v.At.Format(time.RFC3339)
			}
			out.Sites = append(out.Sites, so)
		}
		out.Count = len(out.Sites)
		return nil, out, nil
	})
}

// ---------- list_downloads ----------

func registerDownloads(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_downloads",
		Description: "Read live qBittorrent tasks, including completed downloads still in the downloader. " +
			"Use query to search release names, filter=completed for finished tasks, or hash to check a specific task. " +
			"Returns progress (0-1), raw state, speeds, ETA, paths, and pagination. " +
			"Removed tasks and files outside qBittorrent are not included; an empty result is not proof a resource was never downloaded.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in qb.ListOptions) (*mcp.CallToolResult, qb.ListResult, error) {
		out, err := d.Downloads.List(ctx, in)
		return nil, out, err
	})
}

// ---------- download_torrent ----------

// DownloadInput identifies a resource to submit to the downloader.
type DownloadInput struct {
	ID       string `json:"id" jsonschema:"Resource ID from search_torrents"`
	SavePath string `json:"save_path,omitempty" jsonschema:"Optional downloader save path; leave empty for the configured default"`
}

func registerDownload(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "download_torrent",
		Description: "Submit a resource to qBittorrent to begin downloading. This tool consumes bandwidth and disk space; " +
			"call it only when the user explicitly requests a download. Checks existing tasks first and skips matching downloads, " +
			"returning already_exists and the task status. A successful submission does not mean download completion.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DownloadInput) (*mcp.CallToolResult, download.Result, error) {
		out, err := d.Downloads.Submit(ctx, in.ID, in.SavePath)
		if err != nil {
			out.Error = err.Error()
		}
		return nil, out, nil
	})
}

package qb

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ststyin/torrvia/internal/model"
)

// ErrInvalidOptions identifies invalid list filters or pagination.
var ErrInvalidOptions = errors.New("Invalid download query")

// Torrent contains live downloader facts, using qBittorrent's field names and units.
type Torrent struct {
	Hash           string  `json:"hash" jsonschema:"Downloader task hash; different from a tracker resource ID"`
	Name           string  `json:"name"`
	State          string  `json:"state" jsonschema:"Raw qBittorrent state, such as downloading, stalledUP, stoppedUP, or missingFiles"`
	Progress       float64 `json:"progress" jsonschema:"Fraction of selected files completed, from 0 to 1; missingFiles or error states still need attention"`
	Size           int64   `json:"size" jsonschema:"Selected file size in bytes"`
	TotalSize      int64   `json:"total_size" jsonschema:"All file size in bytes, including unselected files"`
	CompletedBytes int64   `json:"completed" jsonschema:"Completed bytes, not a completion flag"`
	AmountLeft     int64   `json:"amount_left" jsonschema:"Remaining bytes for selected files"`
	DLSpeed        int64   `json:"dlspeed" jsonschema:"Download speed in bytes per second"`
	UPSpeed        int64   `json:"upspeed" jsonschema:"Upload speed in bytes per second"`
	ETA            int64   `json:"eta" jsonschema:"Estimated remaining seconds; 8640000 means unknown"`
	Ratio          float64 `json:"ratio"`
	SavePath       string  `json:"save_path"`
	ContentPath    string  `json:"content_path"`
	AddedOn        int64   `json:"added_on" jsonschema:"Added time as Unix seconds"`
	CompletionOn   int64   `json:"completion_on" jsonschema:"Completion time as Unix seconds; nonpositive means unavailable"`
	Category       string  `json:"category"`
	Tags           string  `json:"tags" jsonschema:"Comma-separated downloader tags"`
}

// ListOptions filters current tasks, including completed tasks still in qBittorrent.
type ListOptions struct {
	Query  string `json:"query,omitempty" jsonschema:"Optional task name search. Use the original release name; localized aliases are not translated"`
	Filter string `json:"filter,omitempty" jsonschema:"State filter: all (default), downloading, completed, active, inactive, stalled, or errored"`
	Hash   string `json:"hash,omitempty" jsonschema:"Optional exact task hash from list_downloads to check one task"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Page size, default 20, maximum 100"`
	Offset int    `json:"offset,omitempty" jsonschema:"Number of matching tasks to skip, default 0"`
}

// ListResult reports pagination after name and state filters, newest tasks first.
type ListResult struct {
	Count   int       `json:"count"`
	Total   int       `json:"total" jsonschema:"Total tasks matching filters, before pagination"`
	Items   []Torrent `json:"items"`
	Limit   int       `json:"limit"`
	Offset  int       `json:"offset"`
	HasMore bool      `json:"has_more"`
}

func (o ListOptions) validate() (ListOptions, error) {
	if o.Filter == "" {
		o.Filter = "all"
	}
	switch o.Filter {
	case "all", "downloading", "completed", "active", "inactive", "stalled", "errored":
	default:
		return o, fmt.Errorf("%w: unsupported filter %q", ErrInvalidOptions, o.Filter)
	}
	if o.Limit < 0 || o.Limit > 100 || o.Offset < 0 {
		return o, fmt.Errorf("%w: limit must be 0-100 and offset must be nonnegative", ErrInvalidOptions)
	}
	if o.Limit == 0 {
		o.Limit = 20
	}
	o.Hash = strings.ToLower(strings.TrimSpace(o.Hash))
	if o.Hash != "" {
		if b, err := hex.DecodeString(o.Hash); err != nil || (len(b) != 20 && len(b) != 32) {
			return o, fmt.Errorf("%w: hash must contain 40 or 64 hexadecimal characters", ErrInvalidOptions)
		}
	}
	return o, nil
}

// List reads current tasks without a cache. Removed tasks and files outside qBittorrent are not included.
func (c *Client) List(ctx context.Context, opts ListOptions) (ListResult, error) {
	opts, err := opts.validate()
	if err != nil {
		return ListResult{}, err
	}
	items, err := c.torrents(ctx, opts.Filter, opts.Hash)
	if err != nil {
		return ListResult{}, err
	}
	matched := make([]Torrent, 0, len(items))
	query := strings.ToLower(strings.TrimSpace(opts.Query))
	normalized := model.NormalizeTitle(query)
	for _, t := range items {
		if query != "" && !strings.Contains(strings.ToLower(t.Name), query) &&
			(normalized == "" || !strings.Contains(model.NormalizeTitle(t.Name), normalized)) {
			continue
		}
		matched = append(matched, t)
	}
	start := min(opts.Offset, len(matched))
	end := start + min(opts.Limit, len(matched)-start)
	return ListResult{
		Count: end - start, Total: len(matched), Items: matched[start:end],
		Limit: opts.Limit, Offset: opts.Offset, HasMore: end < len(matched),
	}, nil
}

func (c *Client) torrents(ctx context.Context, filter, hash string) ([]Torrent, error) {
	var items []Torrent
	err := c.withSession(ctx, func() error {
		params := url.Values{"filter": {filter}, "sort": {"added_on"}, "reverse": {"true"}}
		if hash != "" {
			params.Set("hashes", hash)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v2/torrents/info?"+params.Encode(), nil)
		if err != nil {
			return err
		}
		resp, err := c.request(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			return fmt.Errorf("Failed to decode downloader tasks: %w", err)
		}
		if items == nil {
			return errors.New("Invalid downloader response: expected a task array")
		}
		return nil
	})
	return items, err
}

// FindExisting checks all tasks before submission, including tasks past the first page.
// Hash matches survive renames. Name-and-size matches also catch cross-site copies.
func (c *Client) FindExisting(ctx context.Context, resource model.Torrent) (*Torrent, string, error) {
	items, err := c.torrents(ctx, "all", "")
	if err != nil {
		return nil, "", fmt.Errorf("Could not check existing downloads: %w", err)
	}
	hash := magnetHash(resource.Magnet)
	for _, t := range items {
		if hash != "" && strings.EqualFold(t.Hash, hash) {
			return &t, "hash", nil
		}
	}
	for _, t := range items {
		size := t.TotalSize
		if size <= 0 {
			size = t.Size
		}
		// Require known sizes for heuristic matches; metadata-only tasks need a hash match.
		if size <= 0 || resource.SizeBytes <= 0 {
			continue
		}
		name := t.Name
		for _, ext := range []string{".mkv", ".mp4", ".avi", ".ts"} {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				name = name[:len(name)-len(ext)]
				break
			}
		}
		if model.NormalizeTitle(resource.Title) != "" && resource.SameResource(&model.Torrent{Title: name, SizeBytes: size}) {
			return &t, "name_and_size", nil
		}
	}
	return nil, "", nil
}

func magnetHash(magnet string) string {
	u, err := url.Parse(magnet)
	if err != nil || u.Scheme != "magnet" {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			continue
		}
		hash := xt[len("urn:btih:"):]
		if b, err := hex.DecodeString(hash); err == nil && len(b) == 20 {
			return strings.ToLower(hash)
		}
		if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(hash)); err == nil && len(b) == 20 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

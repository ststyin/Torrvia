// Package download implements the submission workflow shared by the REST and MCP endpoints.
package download

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/model"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
)

var (
	ErrNotConfigured = errors.New("No downloader is configured on the server")
	ErrNotFound      = errors.New("Resource not found. Search again and retry")
	ErrNoSource      = errors.New("This resource has no usable torrent file or magnet link")
)

type Result struct {
	OK            bool        `json:"ok"`
	Via           string      `json:"via,omitempty" jsonschema:"Submission method: torrent_file or magnet"`
	Title         string      `json:"title,omitempty"`
	AlreadyExists bool        `json:"already_exists,omitempty" jsonschema:"Submission was skipped. Inspect existing for the task status"`
	Match         string      `json:"match,omitempty" jsonschema:"Existing task matched by hash or normalized name_and_size"`
	Existing      *qb.Torrent `json:"existing,omitempty"`
	Error         string      `json:"error,omitempty"`
}

type Service struct {
	target  func() (*qb.Client, string)
	search  *search.Service
	mu      sync.Mutex
	pending map[*submission]struct{}
}

type submission struct {
	client   *qb.Client
	resource model.Torrent
	hash     string
	done     chan struct{}
	result   Result
	err      error
}

func New(s *search.Service, target func() (*qb.Client, string)) *Service {
	return &Service{target: target, search: s, pending: make(map[*submission]struct{})}
}

func (s *Service) targetConfig() (*qb.Client, string) {
	if s == nil {
		return nil, ""
	}
	return s.target()
}

func (s *Service) Configured() bool {
	c, _ := s.targetConfig()
	return c != nil
}

func (s *Service) List(ctx context.Context, opts qb.ListOptions) (qb.ListResult, error) {
	c, _ := s.targetConfig()
	if c == nil {
		return qb.ListResult{}, ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.List(ctx, opts)
}

// Submit shares the result of overlapping requests for the same release on the same downloader.
// Once the operation ends, subsequent requests consult the live downloader again.
func (s *Service) Submit(ctx context.Context, id, savePath string) (Result, error) {
	client, defaultPath := s.targetConfig()
	if savePath == "" {
		savePath = defaultPath
	}
	if client == nil {
		return Result{}, ErrNotConfigured
	}
	t, src, ok := s.search.Resource(id)
	if !ok {
		return Result{}, ErrNotFound
	}

	hash := qb.MagnetHash(t.Magnet)
	s.mu.Lock()
	for job := range s.pending {
		if job.client == client && (job.resource.ID == id || (hash != "" && hash == job.hash) ||
			(t.SizeBytes > 0 && job.resource.SizeBytes > 0 && t.SameResource(&job.resource))) {
			s.mu.Unlock()
			select {
			case <-job.done:
				return job.result, job.err
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
	}
	job := &submission{client: client, resource: t, hash: hash, done: make(chan struct{})}
	s.pending[job] = struct{}{}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	job.result, job.err = submit(ctx, client, t, src, savePath)
	s.mu.Lock()
	delete(s.pending, job)
	close(job.done)
	s.mu.Unlock()
	return job.result, job.err
}

func submit(ctx context.Context, c *qb.Client, t model.Torrent, src adapter.Adapter, path string) (Result, error) {
	existing, match, err := c.FindExisting(ctx, t)
	if err != nil {
		return Result{}, err
	}
	if existing != nil {
		return Result{OK: true, Title: t.Title, AlreadyExists: true, Existing: existing, Match: match}, nil
	}
	if f, ok := src.(adapter.TorrentFetcher); ok && t.DownloadURL != "" {
		data, name, err := f.FetchTorrent(ctx, t)
		if err == nil && len(data) > 0 {
			if err := c.AddFile(ctx, data, name, path); err != nil {
				return Result{}, err
			}
			return Result{OK: true, Title: t.Title, Via: "torrent_file"}, nil
		}
		if err != nil && t.Magnet == "" {
			return Result{}, fmt.Errorf("Failed to retrieve torrent: %w", err)
		}
	}
	if t.Magnet == "" {
		return Result{}, ErrNoSource
	}
	if err := c.AddLink(ctx, t.Magnet, path); err != nil {
		return Result{}, err
	}
	return Result{OK: true, Title: t.Title, Via: "magnet"}, nil
}

// Package store appends search history to JSONL files without requiring a database.
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Record is a search history entry.
type Record struct {
	At      time.Time `json:"at"`
	Query   string    `json:"query"`
	Filters string    `json:"filters,omitempty"`
	Total   int       `json:"total"`
	Elapsed int64     `json:"elapsed_ms"`
}

// History stores append-only search records.
type History struct {
	mu   sync.Mutex
	path string
}

// NewHistory prepares the history file path in the data directory.
func NewHistory(dataDir string) (*History, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &History{path: filepath.Join(dataDir, "history.jsonl")}, nil
}

// Append writes a record; failures do not need to interrupt search requests.
func (h *History) Append(r Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(r)
}

// Recent returns the most recent n records, newest first.
func (h *History) Recent(n int) ([]Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	f, err := os.Open(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var all []Record
	next := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if n > 0 && len(all) == n {
			all[next] = r
			next = (next + 1) % n
		} else {
			all = append(all, r)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// The ring's two segments each run oldest first. Reverse them separately
	// so the last inserted entry leads the output.
	slices.Reverse(all[:next])
	slices.Reverse(all[next:])
	return all, nil
}

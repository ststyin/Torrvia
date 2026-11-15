// Package model defines normalized tracker data. Title recognition, translation, and other semantic decisions belong to the calling AI.
package model

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Promo represents a site's promotion status.
type Promo string

const (
	PromoNone   Promo = "none"   // Standard
	PromoFree   Promo = "free"   // Freeleech
	Promo2x     Promo = "2x"     // Double upload credit
	Promo2xFree Promo = "2xfree" // Freeleech with double upload credit
	Promo50     Promo = "50%"    // 50% download credit
	Promo30     Promo = "30%"    // 30% download credit
)

// Weight favors promotions that reduce download credit consumption.
func (p Promo) Weight() float64 {
	switch p {
	case Promo2xFree:
		return 3.5
	case PromoFree:
		return 3.0
	case Promo2x:
		return 2.5
	case Promo50:
		return 1.6
	case Promo30:
		return 1.3
	default:
		return 1.0
	}
}

// IsFree reports whether downloading consumes no download credit.
func (p Promo) IsFree() bool {
	return p == PromoFree || p == Promo2xFree
}

// Label returns a human-readable promotion label.
func (p Promo) Label() string {
	switch p {
	case Promo2xFree:
		return "Freeleech + 2X"
	case PromoFree:
		return "Freeleech"
	case Promo2x:
		return "2X"
	case Promo50:
		return "50%"
	case Promo30:
		return "30%"
	default:
		return "Standard"
	}
}

// Torrent is a normalized resource record.
type Torrent struct {
	ID          string   `json:"id"`                 // Format: "site-id:torrent-id"
	Site        string   `json:"site"`               // Site ID
	SiteName    string   `json:"site_name"`          // Site display name
	Title       string   `json:"title"`              // Original release name
	SubTitle    string   `json:"subtitle,omitempty"` // Site-provided localized title or description
	Tags        []string `json:"tags,omitempty"`     // Site-provided subtitle, audio, and release labels
	SizeBytes   int64    `json:"size_bytes"`         // Size in bytes
	Seeders     int      `json:"seeders"`            // Seeders
	Leechers    int      `json:"leechers"`           // Leechers
	Snatched    int      `json:"snatched,omitempty"`
	Promo       Promo    `json:"promo"`                // Promotion status
	Resolution  string   `json:"resolution,omitempty"` // 2160p / 1080p / 720p
	PublishTime string   `json:"publish_time,omitempty"`
	DetailURL   string   `json:"detail_url,omitempty"`
	DownloadURL string   `json:"download_url,omitempty"`
	Magnet      string   `json:"magnet,omitempty"`
	Score       float64  `json:"score"`             // Ranking score
	AlsoOn      []string `json:"also_on,omitempty"` // IDs of duplicate resources on other sites
}

// Recompute scores resources by seeders multiplied by promotion weight.
func (t *Torrent) Recompute() {
	raw := float64(t.Seeders) * t.Promo.Weight()
	t.Score = math.Round(raw*100) / 100
}

// HumanSize formats the torrent size for display.
func (t *Torrent) HumanSize() string {
	return HumanBytes(t.SizeBytes)
}

// HumanBytes formats a byte count for display.
func HumanBytes(b int64) string {
	if b <= 0 {
		return "Unknown"
	}
	const unit = 1024
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(b)
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if i >= 3 {
		return fmt.Sprintf("%.2f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

var resolutionRe = regexp.MustCompile(`(?i)\b(2160p|1080[pi]|720p|480p)\b`)

// DetectResolution returns a resolution found in a release name, or an empty string.
func DetectResolution(title string) string {
	if m := resolutionRe.FindStringSubmatch(title); len(m) > 1 {
		r := strings.ToLower(m[1])
		if r == "1080i" {
			return "1080p"
		}
		return r
	}
	return ""
}

var nonWordRe = regexp.MustCompile(`[^\p{Han}a-z0-9]+`)

// NormalizeTitle creates a deterministic fingerprint without semantic title inference.
func NormalizeTitle(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&amp;", "and")
	s = nonWordRe.ReplaceAllString(s, "")
	return s
}

// SameResource compares title fingerprints and allows an 8% size difference for cross-site copies.
func (t *Torrent) SameResource(o *Torrent) bool {
	if NormalizeTitle(t.Title) != NormalizeTitle(o.Title) {
		return false
	}
	if t.SizeBytes <= 0 || o.SizeBytes <= 0 {
		return true
	}
	diff := math.Abs(float64(t.SizeBytes-o.SizeBytes)) / float64(t.SizeBytes)
	return diff <= 0.08
}

// Query contains search parameters.
type Query struct {
	Keyword    string   `json:"keyword"`
	Year       int      `json:"year,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	FreeOnly   bool     `json:"free_only,omitempty"`
	MinSeeders int      `json:"min_seeders,omitempty"`
	Sites      []string `json:"sites,omitempty"` // Empty means all enabled sites
	Limit      int      `json:"limit,omitempty"`
}

// Match checks the query's hard filters.
func (q Query) Match(t *Torrent) bool {
	if q.FreeOnly && !t.Promo.IsFree() {
		return false
	}
	if q.MinSeeders > 0 && t.Seeders < q.MinSeeders {
		return false
	}
	if q.Resolution != "" && !strings.EqualFold(t.Resolution, q.Resolution) {
		return false
	}
	if q.Year > 0 {
		// Year matching is intentionally loose; strict release matching requires external metadata.
		if !strings.Contains(t.Title, fmt.Sprint(q.Year)) {
			return false
		}
	}
	return true
}

// SiteStatus describes the current availability of a site.
type SiteStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	// Checked distinguishes an untested site from a failed check.
	Checked   bool   `json:"checked"`
	OK        bool   `json:"ok"`
	CheckedAt string `json:"checked_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
)

// nexusphp handles tracker variations in search paths, promotion icons, and table layouts. Column classes are preferred; positional parsing falls back to numeric cells after the size column.
type nexusphp struct {
	site   config.Site
	origin *url.URL
	client *http.Client
}

func newNexusPHP(site config.Site) *nexusphp {
	base, _ := url.Parse(site.BaseURL)
	n := &nexusphp{
		origin: base,
		site:   site,
		client: &http.Client{
			Timeout: time.Duration(site.TimeoutSec) * time.Second,
		},
	}
	n.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("Too many tracker redirects")
		}
		n.trackerCredentials(req)
		return nil
	}
	return n
}

func (n *nexusphp) ID() string   { return n.site.ID }
func (n *nexusphp) Name() string { return n.site.Name }
func (n *nexusphp) Type() string { return "nexusphp" }

func (n *nexusphp) setHeaders(req *http.Request) {
	ua := n.site.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "max-age=0")
	n.trackerCredentials(req)
}

// Apply credentials to the exact tracker origin. Go's default redirect policy
// also forwards cookies to subdomains, which may be separate download hosts.
func (n *nexusphp) trackerCredentials(req *http.Request) {
	if n.origin != nil && req.URL.Scheme == n.origin.Scheme &&
		strings.EqualFold(req.URL.Hostname(), n.origin.Hostname()) && originPort(req.URL) == originPort(n.origin) {
		req.Header.Set("Cookie", n.site.Cookie)
		req.Header.Set("Referer", n.site.BaseURL+"/")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		return
	}
	req.Header.Del("Cookie")
	req.Header.Del("Authorization")
	req.Header.Del("Referer")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.URL.User = nil
}

func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// fetch uses the site's credentials and rejects partial reads above the allowed size.
func (n *nexusphp) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	n.setHeaders(req)
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("Request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("Site returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to read response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, nil, fmt.Errorf("Site response exceeds %d bytes", limit)
	}
	return body, resp.Request.URL, nil
}

func (n *nexusphp) get(ctx context.Context, rawURL string) ([]byte, error) {
	body, finalURL, err := n.fetch(ctx, rawURL, 8<<20)
	if err != nil {
		return nil, err
	}
	// Detect challenge pages before parsing: some return HTTP 200 instead of an error.
	if isCloudflareChallenge(string(body)) {
		return nil, fmt.Errorf("Blocked by a Cloudflare challenge: cf_clearance has expired or does not match the current IP or User-Agent")
	}
	// A login-page redirect means the supplied credentials were rejected.
	if strings.Contains(finalURL.Path, "login") || looksLikeLogin(string(body)) {
		return nil, n.authError()
	}
	return body, nil
}

func isCloudflareChallenge(body string) bool {
	return strings.Contains(body, "Just a moment...") ||
		strings.Contains(body, "challenges.cloudflare.com") ||
		strings.Contains(body, "cf-challenge")
}

// authError explains incomplete or expired cookies. Session-only NexusPHP logins require PHPSESSID when Remember me is disabled.
func (n *nexusphp) authError() error {
	ck := n.site.Cookie
	hasSession := strings.Contains(ck, "PHPSESSID")
	sessionOnly := strings.Contains(ck, "c_secure_login=nope") ||
		strings.Contains(ck, "c_secure_login%3Dnope")

	switch {
	case sessionOnly && !hasSession:
		return fmt.Errorf("Incomplete cookie: c_secure_login is nope (Remember me is disabled). " +
			"The session requires PHPSESSID, which is missing from the copied cookie. " +
			"Log in with Remember me enabled and copy the complete Cookie header, or include PHPSESSID")
	case !hasSession:
		return fmt.Errorf("Incomplete cookie: PHPSESSID is missing. Copy the complete Cookie request header")
	case strings.Contains(ck, "c_secure_uid"):
		return fmt.Errorf("Invalid cookie: PHPSESSID may have expired. Log in again and copy a fresh Cookie header")
	default:
		return fmt.Errorf("Invalid cookie: required authentication fields are missing")
	}
}

func looksLikeLogin(body string) bool {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "login.php?returnto") {
		return true
	}
	// Login pages usually contain a password input without torrent links.
	return strings.Contains(lower, `type="password"`) && !strings.Contains(lower, "details.php?id=")
}

// DumpPage returns the raw search HTML for the site debugging command.
func (n *nexusphp) DumpPage(ctx context.Context, keyword string) ([]byte, error) {
	u, err := url.Parse(n.site.BaseURL + n.site.SearchPath)
	if err != nil {
		return nil, fmt.Errorf("Invalid tracker search URL: %w", err)
	}
	params := u.Query()
	params.Set("search", keyword)
	params.Set("search_area", "0")
	params.Set("search_mode", "0")
	u.RawQuery = params.Encode()
	return n.get(ctx, u.String())
}

func (n *nexusphp) Search(ctx context.Context, q model.Query) ([]model.Torrent, error) {
	keyword := strings.TrimSpace(q.Keyword)
	if keyword == "" {
		return nil, nil
	}
	body, err := n.DumpPage(ctx, keyword)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("Failed to parse HTML: %w", err)
	}
	return n.parse(doc), nil
}

func (n *nexusphp) Health(ctx context.Context) error {
	_, err := n.get(ctx, n.site.BaseURL+"/index.php")
	return err
}

// FetchTorrent retrieves a torrent with site credentials; only the torrent bytes are passed to the downloader.
func (n *nexusphp) FetchTorrent(ctx context.Context, t model.Torrent) ([]byte, string, error) {
	if t.DownloadURL == "" {
		return nil, "", fmt.Errorf("This resource has no torrent download URL")
	}
	data, _, err := n.fetch(ctx, t.DownloadURL, 16<<20)
	if err != nil {
		return nil, "", err
	}
	// A bencoded dictionary starts with 'd'; use it as a basic validity check.
	if len(data) == 0 || data[0] != 'd' {
		return nil, "", fmt.Errorf("Response is not a torrent file; the cookie may have expired")
	}

	filename := t.Site + "-" + strings.TrimPrefix(t.ID, t.Site+":") + ".torrent"
	return data, filename, nil
}

var (
	idRe      = regexp.MustCompile(`details\.php\?id=(\d+)`)
	sizeParse = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(B|KB|MB|GB|TB|PB)$`)
	dateRe    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	digitsRe  = regexp.MustCompile(`^\d+$`)
)

func (n *nexusphp) parse(doc *goquery.Document) []model.Torrent {
	// Limit rows to the torrent table where possible to avoid duplicates from nested tables.
	rows := doc.Find("table.torrents tr, table.torrents-table tr")
	if rows.Length() == 0 {
		rows = doc.Find("tr")
	}

	out := make([]model.Torrent, 0, 64)
	seen := make(map[string]bool, 64)

	rows.Each(func(_ int, row *goquery.Selection) {
		t, ok := n.parseRow(row)
		if !ok || seen[t.ID] {
			return
		}
		seen[t.ID] = true
		out = append(out, t)
	})

	return out
}

func (n *nexusphp) parseRow(row *goquery.Selection) (model.Torrent, bool) {
	// Prefer explicit data attributes over values inferred from table cells.
	dataID := strings.TrimSpace(row.AttrOr("data-torrent-id", ""))
	dataTitle := strings.TrimSpace(row.AttrOr("data-title", ""))
	dataDesc := strings.TrimSpace(row.AttrOr("data-description", ""))

	var id, title string
	links := row.Find(`a[href*="details.php?id="]`)
	links.Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		m := idRe.FindStringSubmatch(href)
		if m == nil {
			return
		}
		text := strings.TrimSpace(a.Text())
		// Skip numeric links for peer counts rather than treating them as titles.
		if text == "" || digitsRe.MatchString(text) || strings.EqualFold(text, "详情") {
			return
		}
		// The longest matching link text is the title.
		if len(text) > len(title) {
			title, id = text, m[1]
		}
	})
	if id == "" {
		id = dataID
	}
	if title == "" {
		title = dataTitle
	}
	if id == "" || title == "" {
		return model.Torrent{}, false
	}

	// Some templates store the complete title in an attribute.
	if v := strings.TrimSpace(row.Find("a.torrent-title-link").First().AttrOr("title", "")); v != "" {
		title = v
	}
	// The site's explicit release title takes precedence.
	if dataTitle != "" {
		title = dataTitle
	}

	t := model.Torrent{
		ID:          n.site.ID + ":" + id,
		Site:        n.site.ID,
		SiteName:    n.site.Name,
		Title:       title,
		Promo:       model.PromoNone,
		Resolution:  model.DetectResolution(title),
		DetailURL:   fmt.Sprintf("%s/details.php?id=%s", n.site.BaseURL, id),
		DownloadURL: fmt.Sprintf("%s/download.php?id=%s", n.site.BaseURL, id),
	}

	if !n.parseByColumnClass(row, &t) {
		n.parseByPosition(row, &t)
	}

	// Backend-provided attributes take precedence over inferred values.
	if v, ok := attrInt(row, "data-seeders"); ok {
		t.Seeders = v
	}
	if v, ok := attrInt(row, "data-leechers"); ok {
		t.Leechers = v
	}
	if v, ok := attrInt(row, "data-times-completed"); ok {
		t.Snatched = v
	}
	if v, ok := attrInt64(row, "data-size-bytes"); ok && v > 0 {
		t.SizeBytes = v
	}
	if v, ok := attrInt64(row, "data-added-ts"); ok && v > 0 {
		// Convert epoch milliseconds to the site's UTC+8 time zone, regardless of the server's time zone.
		t.PublishTime = time.UnixMilli(v).In(cstZone).Format("2006-01-02 15:04:05")
	}

	// Localized titles and descriptions are often separate from the release name.
	if dataDesc != "" && !strings.EqualFold(dataDesc, title) {
		t.SubTitle = dataDesc
	} else if v := strings.TrimSpace(row.Find(".torrent-subtitle-text").First().Text()); v != "" && !strings.EqualFold(v, title) {
		t.SubTitle = v
	}
	t.Tags = parseTags(row)

	row.Find("img").Each(func(_ int, img *goquery.Selection) {
		probe := strings.ToLower(img.AttrOr("class", "") + " " + img.AttrOr("alt", "") + " " + img.AttrOr("title", ""))
		if p := classifyPromo(probe); p.Weight() > t.Promo.Weight() {
			t.Promo = p
		}
	})

	row.Find(`a[href^="magnet:"]`).Each(func(_ int, a *goquery.Selection) {
		if href, ok := a.Attr("href"); ok && t.Magnet == "" {
			t.Magnet = href
		}
	})

	if t.SizeBytes == 0 && t.Seeders == 0 {
		// Rows without both size and seeders are unlikely to be torrents.
		return model.Torrent{}, false
	}

	t.Recompute()
	return t, true
}

// parseByColumnClass handles templates with explicit size and peer column classes.
func (n *nexusphp) parseByColumnClass(row *goquery.Selection, t *model.Torrent) bool {
	sizeSel := row.Find("td.torrent-size-cell").First()
	if sizeSel.Length() == 0 {
		return false
	}
	if v, ok := findSizeInText(sizeSel.Text()); ok {
		t.SizeBytes = v
	}
	t.Seeders = atoiSafe(textOf(row, "td.torrent-seeders-cell"))
	t.Leechers = atoiSafe(textOf(row, "td.torrent-leechers-cell"))
	t.Snatched = atoiSafe(textOf(row, "td.torrent-completed-cell"))
	t.PublishTime = findTimestamp(row.Find("td.torrent-time-cell").First())
	return true
}

// parseByPosition locates size and subsequent peer counts in older table layouts.
func (n *nexusphp) parseByPosition(row *goquery.Selection, t *model.Torrent) {
	var counts []int
	foundSize := false
	row.Find("td").Each(func(_ int, td *goquery.Selection) {
		text := strings.TrimSpace(td.Text())
		if t.PublishTime == "" {
			t.PublishTime = findTimestamp(td)
		}
		if !foundSize {
			if size, ok := parseSizeFromTD(td, text); ok {
				t.SizeBytes, foundSize = size, true
				// Peer columns follow size. Without a size column, fall back to
				// the first two numeric cells as on older templates.
				counts = counts[:0]
				return
			}
		}
		if len(counts) < 3 && digitsRe.MatchString(text) {
			if value, err := strconv.Atoi(text); err == nil {
				counts = append(counts, value)
			}
		}
	})
	if foundSize {
		if len(counts) >= 1 {
			t.Seeders = counts[0]
		}
		if len(counts) >= 2 {
			t.Leechers = counts[1]
		}
		if len(counts) >= 3 {
			t.Snatched = counts[2]
		}
	} else if len(counts) >= 2 {
		t.Seeders, t.Leechers = counts[0], counts[1]
	}

}

func textOf(sel *goquery.Selection, selector string) string {
	return strings.TrimSpace(sel.Find(selector).First().Text())
}

// cstZone keeps displayed tracker timestamps independent of the server's time zone.
var cstZone = time.FixedZone("CST", 8*3600)

// attrInt reports presence separately because zero is a valid peer count.
func attrInt(sel *goquery.Selection, name string) (int, bool) {
	raw, exists := sel.Attr(name)
	if !exists {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return v, true
}

func attrInt64(sel *goquery.Selection, name string) (int64, bool) {
	raw, exists := sel.Attr(name)
	if !exists {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseTags extracts subtitle, audio-language, and official-release labels used to select a resource.
func parseTags(row *goquery.Selection) []string {
	var out []string
	seen := map[string]bool{}
	row.Find(".torrent-subtitle-tags .tags, .torrent-subtitle-tags .tag").Each(func(_ int, s *goquery.Selection) {
		label := strings.TrimSpace(s.Text())
		if label == "" {
			// Icon-only labels may store their text in the title attribute.
			label = strings.TrimSpace(s.AttrOr("title", ""))
		}
		label = strings.TrimSpace(strings.TrimSuffix(label, "资源"))
		label = strings.TrimSuffix(label, "配音")
		label = strings.TrimSuffix(label, "字幕")
		if label == "" || seen[label] {
			return
		}
		seen[label] = true
		out = append(out, label)
	})
	return out
}

func atoiSafe(s string) int {
	if !digitsRe.MatchString(s) {
		return 0
	}
	v, _ := strconv.Atoi(s)
	return v
}

var sizeInTextRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(B|KB|MB|GB|TB|PB)`)

// findSizeInText accepts sizes split across lines or surrounded by other text.
func findSizeInText(s string) (int64, bool) {
	m := sizeInTextRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	return parseSize(m[1] + " " + m[2])
}

func parseSizeFromTD(td *goquery.Selection, text string) (int64, bool) {
	if b, ok := parseSize(td.AttrOr("title", "")); ok {
		return b, true
	}
	td.Find("[title]").EachWithBreak(func(_ int, el *goquery.Selection) bool {
		v := strings.TrimSpace(el.AttrOr("title", ""))
		if sizeParse.MatchString(v) {
			text = v
			return false
		}
		return true
	})
	return parseSize(text)
}

func parseSize(s string) (int64, bool) {
	m := sizeParse.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{
		"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40, "PB": 1 << 50,
	}
	return int64(f * mult[strings.ToUpper(m[2])]), true
}

func findTimestamp(td *goquery.Selection) string {
	var found string
	td.Find("[title]").EachWithBreak(func(_ int, el *goquery.Selection) bool {
		if v := el.AttrOr("title", ""); dateRe.MatchString(v) {
			found = v
			return false
		}
		return true
	})
	if found != "" {
		return found
	}
	if t := strings.TrimSpace(td.Text()); dateRe.MatchString(t) && len(t) < 24 {
		return t
	}
	return ""
}

// classifyPromo matches combined promotions before individual flags.
func classifyPromo(probe string) model.Promo {
	hasFree := strings.Contains(probe, "pro_free") || strings.Contains(probe, "免费") || strings.Contains(probe, "free")
	has2x := strings.Contains(probe, "pro_2up") || strings.Contains(probe, "2x") || strings.Contains(probe, "2up")

	if hasFree && has2x {
		return model.Promo2xFree
	}
	if hasFree {
		return model.PromoFree
	}
	if has2x {
		return model.Promo2x
	}
	if strings.Contains(probe, "pro_50pctdown") || strings.Contains(probe, "50%") {
		return model.Promo50
	}
	if strings.Contains(probe, "pro_30pctdown") || strings.Contains(probe, "30%") {
		return model.Promo30
	}
	return model.PromoNone
}

// Package qb reads qBittorrent tasks and submits magnet links and torrent files.
package qb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var errSessionExpired = errors.New("Downloader session expired")

// Client maintains a qBittorrent session.
type Client struct {
	base string
	user string
	pass string
	http *http.Client

	mu  sync.Mutex
	sid string
}

// New creates a client and bypasses environment proxies for the local downloader to avoid misleading routing failures.
func New(base, user, pass string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &Client{
		base: strings.TrimRight(base, "/"),
		user: user,
		pass: pass,
		http: &http.Client{Timeout: 30 * time.Second, Transport: tr,
			// A redirect is not an API response. In particular a 307 may replay the login form.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *Client) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.user)
	form.Set("password", c.pass)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Failed to connect to downloader: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("Downloader login failed; check the username and password")
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "SID" {
			c.sid = ck.Value
			return nil
		}
	}
	return fmt.Errorf("Downloader did not return a session cookie")
}

// AddLink submits a magnet link, using the downloader's default directory when savePath is empty.
func (c *Client) AddLink(ctx context.Context, magnet, savePath string) error {
	return c.withSession(ctx, func() error { return c.postForm(ctx, magnet, savePath) })
}

// AddFile uploads torrent bytes retrieved by the service using tracker credentials.
func (c *Client) AddFile(ctx context.Context, data []byte, filename, savePath string) error {
	return c.withSession(ctx, func() error { return c.postFile(ctx, data, filename, savePath) })
}

// Ping verifies credentials with a fresh session.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sid = ""
	return c.login(ctx)
}

// Probe submits an empty task to verify the submission route. HTTP 200 with Fails. confirms the route is reachable; HTTP 404 indicates a routing problem.
func (c *Client) Probe(ctx context.Context) error {
	return c.withSession(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v2/torrents/add", strings.NewReader("urls="))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.request(req)
		if err != nil {
			return fmt.Errorf("Task submission route: %w", err)
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	})
}

func (c *Client) withSession(ctx context.Context, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sid == "" {
		if err := c.login(ctx); err != nil {
			return err
		}
	}
	err := fn()
	if !errors.Is(err, errSessionExpired) {
		return err
	}
	// The session may have expired; authenticate and retry once.
	c.sid = ""
	if lerr := c.login(ctx); lerr != nil {
		return lerr
	}
	return fn()
}

func (c *Client) postForm(ctx context.Context, link, savePath string) error {
	form := url.Values{}
	form.Set("urls", link)
	if savePath != "" {
		form.Set("savepath", savePath)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/v2/torrents/add", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

func (c *Client) postFile(ctx context.Context, data []byte, filename, savePath string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("torrents", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if savePath != "" {
		if err := mw.WriteField("savepath", savePath); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/v2/torrents/add", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.do(req)
}

func (c *Client) do(req *http.Request) error {
	resp, err := c.request(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("Failed to read downloader response: %w", err)
	}
	// qBittorrent may reject a task with HTTP 200 and a Fails. response.
	if strings.Contains(strings.ToLower(string(body)), "fail") {
		return fmt.Errorf("Downloader rejected the task: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) request(req *http.Request) (*http.Response, error) {
	req.Header.Set("Referer", c.base)
	req.AddCookie(&http.Cookie{Name: "SID", Value: c.sid})

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Failed to connect to downloader: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return nil, errSessionExpired
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Include a short response body because qBittorrent reports routing and parameter errors as plain text.
		snippet := strings.TrimSpace(string(body))
		if r := []rune(snippet); len(r) > 120 {
			snippet = string(r[:120]) + "…"
		}
		if snippet == "" {
			snippet = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("Downloader returned HTTP %d: %s", resp.StatusCode, snippet)
	}
	return resp, nil
}

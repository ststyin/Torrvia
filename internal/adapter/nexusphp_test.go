package adapter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchRejectsTruncatedResponse(t *testing.T) {
	site := newNexusPHP(config.Site{BaseURL: "https://test.invalid", Cookie: "session=test"})
	site.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "session=test" {
			t.Fatal("missing tracker cookie")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("12345")), Request: req}, nil
	})
	for _, limit := range []int64{4, 5} {
		data, _, err := site.fetch(context.Background(), site.site.BaseURL, limit)
		if limit == 4 && (err == nil || len(data) > 0) {
			t.Fatal("oversized response accepted")
		}
		if limit == 5 && (err != nil || string(data) != "12345") {
			t.Fatalf("exact-size response failed: %v, %v", data, err)
		}
	}
}

func TestPeerCountsByPosition(t *testing.T) {
	for _, tc := range []struct {
		cells             string
		seed, leech, done int
	}{
		{"<td>99</td><td title='1 GB'>1</td><td>12</td><td>5</td><td>9</td>", 12, 5, 9},
		{"<td>99</td><td>1 GB</td><td>8</td>", 8, 0, 0},
		{"<td>12</td><td>5</td><td>9</td>", 12, 5, 0},
	} {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader("<table><tr>" + tc.cells + "</tr></table>"))
		if err != nil {
			t.Fatal(err)
		}
		var item model.Torrent
		new(nexusphp).parseByPosition(doc.Find("tr"), &item)
		if item.Seeders != tc.seed || item.Leechers != tc.leech || item.Snatched != tc.done {
			t.Errorf("counts parsed incorrectly for %s: %+v", tc.cells, item)
		}
	}
}

func TestCredentialsFollowTrackerOrigin(t *testing.T) {
	const base = "https://tracker.invalid"
	for _, tc := range []struct {
		name         string
		target       string
		credentialed bool
	}{
		{"same origin", base + ":443/content", true},
		{"separate host", "https://cdn.invalid/content", false},
		{"same host but another port", base + ":9443/content", false},
		{"download subdomain", "https://cdn.tracker.invalid/content", false},
		{"HTTP downgrade", "http://tracker.invalid/content", false},
	} {
		for _, redirect := range []bool{false, true} {
			t.Run(fmt.Sprint(tc.name, "/redirect=", redirect), func(t *testing.T) {
				n := newNexusPHP(config.Site{BaseURL: base, Cookie: "session=test"})
				n.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					resp := &http.Response{Request: req, StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("de"))}
					cred := tc.credentialed
					if req.URL.Path == "/start" {
						cred = true
						resp.StatusCode = http.StatusFound
						resp.Header.Set("Location", tc.target)
					}
					if got := req.Header.Get("Cookie"); (cred && got != "session=test") || (!cred && got != "") {
						t.Errorf("incorrect cookie handling for %s", req.URL)
					}
					if !cred && req.Header.Get("Referer") != "" {
						t.Errorf("tracker referer leaked to %s", req.URL)
					}
					return resp, nil
				})
				target := tc.target
				if redirect {
					target = base + "/start"
				}
				data, _, err := n.FetchTorrent(context.Background(), model.Torrent{DownloadURL: target})
				if err != nil || string(data) != "de" {
					t.Fatalf("download failed: %v", err)
				}
			})
		}
	}
}

func TestSearchPreservesConfiguredParameters(t *testing.T) {
	n := newNexusPHP(config.Site{BaseURL: "https://tracker.invalid", SearchPath: "/torrents.php?category=film&search=old"})
	n.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if q.Get("category") != "film" || !slices.Equal(q["search"], []string{"中文 & test"}) || q.Get("search_mode") != "0" {
			t.Errorf("bad query %s", req.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Request: req, Body: io.NopCloser(strings.NewReader("<html></html>"))}, nil
	})
	_, err := n.Search(context.Background(), model.Query{Keyword: "中文 & test"})
	if err != nil {
		t.Fatal(err)
	}
}

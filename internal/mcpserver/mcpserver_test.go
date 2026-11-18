package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/download"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
)

func testSession(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(Handler(d))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		if result.IsError {
			t.Fatalf("tool error: %+v", result.Content)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestListDownloadsTool(t *testing.T) {
	qbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
			fmt.Fprint(w, "Ok.")
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("filter") != "completed" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `[{"hash":"`+strings.Repeat("a", 40)+`","name":"Fights.Break.Sphere.S05E219","state":"stalledUP","progress":1,"save_path":"/media"}]`)
	}))
	defer qbsrv.Close()
	client := qb.New(qbsrv.URL, "", "")
	session := testSession(t, Deps{Downloads: download.New(nil, func() (*qb.Client, string) { return client, "" })})
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "list_downloads" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("download listing must be marked read-only")
			}
		}
	}
	if !found {
		t.Fatal("list_downloads was not advertised")
	}
	var out qb.ListResult
	callTool(t, session, "list_downloads", map[string]any{"query": "Fights Break Sphere", "filter": "completed"}, &out)
	if out.Count != 1 || out.Items[0].Progress != 1 || out.Items[0].SavePath != "/media" {
		t.Fatalf("unexpected tool output: %+v", out)
	}
	if result := callTool(t, session, "list_downloads", map[string]any{"filter": "invalid"}, nil); !result.IsError {
		t.Fatal("invalid filters must produce a tool error")
	}
}

func TestListDownloadsWithoutDownloader(t *testing.T) {
	session := testSession(t, Deps{})
	result := callTool(t, session, "list_downloads", map[string]any{}, nil)
	if !result.IsError {
		t.Fatal("missing downloader must be an error, not an empty task list")
	}
}

func TestDownloadChecksExistingBeforeSubmission(t *testing.T) {
	a, err := adapter.Build(config.Site{ID: "demo", Name: "Demo", Type: "mock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := search.New([]adapter.Adapter{a}, 0)
	for _, mode := range []string{"existing", "new", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var adds atomic.Int32
			var results SearchOutput
			qbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
					fmt.Fprint(w, "Ok.")
				case "/api/v2/torrents/info":
					if mode == "unavailable" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					if mode == "new" {
						fmt.Fprint(w, "[]")
						return
					}
					item := results.Items[0]
					json.NewEncoder(w).Encode([]qb.Torrent{{Hash: strings.Repeat("a", 40), Name: item.Title, TotalSize: int64(item.SizeGB * (1 << 30)), Progress: .75, State: "downloading"}})
				case "/api/v2/torrents/add":
					adds.Add(1)
					if err := r.ParseForm(); err != nil || !strings.HasPrefix(r.Form.Get("urls"), "magnet:") || r.Form.Get("savepath") != "/configured" {
						t.Error("new download lost its magnet or configured save path")
					}
					fmt.Fprint(w, "Ok.")
				default:
					t.Errorf("unexpected downloader request: %s", r.URL.Path)
				}
			}))
			defer qbsrv.Close()
			client := qb.New(qbsrv.URL, "", "")
			session := testSession(t, Deps{Svc: svc, Downloads: download.New(svc, func() (*qb.Client, string) { return client, "/configured" })})
			callTool(t, session, "search_torrents", map[string]any{"query": "Interstellar"}, &results)
			if len(results.Items) == 0 {
				t.Fatal("expected a search result")
			}
			var out download.Result
			callTool(t, session, "download_torrent", map[string]any{"id": results.Items[0].ID}, &out)
			switch mode {
			case "existing":
				if !out.OK || !out.AlreadyExists || out.Existing == nil || out.Existing.Progress != .75 || adds.Load() != 0 {
					t.Fatalf("duplicate was resubmitted: %+v, adds=%d", out, adds.Load())
				}
			case "new":
				if !out.OK || out.AlreadyExists || out.Via != "magnet" || adds.Load() != 1 {
					t.Fatalf("new download not submitted: %+v, adds=%d", out, adds.Load())
				}
			case "unavailable":
				if out.OK || out.Error == "" || adds.Load() != 0 {
					t.Fatalf("submission must stop when checking fails: %+v", out)
				}
			}
		})
	}
}

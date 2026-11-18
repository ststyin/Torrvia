package qb

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ststyin/torrvia/internal/model"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "user" || r.Form.Get("password") != "password" {
				t.Error("missing login credentials")
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-session"})
			fmt.Fprint(w, "Ok.")
			return
		}
		cookie, err := r.Cookie("SID")
		if err != nil || cookie.Value != "test-session" || r.Header.Get("Referer") == "" {
			t.Error("missing downloader session or Referer")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "user", "password")
}

func TestListFiltersBeforePagination(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("sort") != "added_on" || r.URL.Query().Get("reverse") != "true" {
			t.Errorf("unexpected list request: %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Has("limit") || r.URL.Query().Has("offset") {
			t.Error("name filtering must happen before pagination")
		}
		json.NewEncoder(w).Encode([]Torrent{
			{Name: "Other.Show.S01E01"},
			{Hash: strings.Repeat("a", 40), Name: "Fights.Break.Sphere.S05E219", State: "downloading", Progress: .5, DLSpeed: 1024, SavePath: "/data"},
			{Hash: strings.Repeat("b", 40), Name: "Fights Break Sphere S05E218", State: "stalledUP", Progress: 1},
			{Hash: strings.Repeat("c", 40), Name: "Fights Break Sphere S05E217"},
		})
	})
	got, err := client.List(context.Background(), ListOptions{Query: "fights break sphere", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Total != 3 || !got.HasMore || got.Items[0].Hash != strings.Repeat("b", 40) || got.Items[0].Progress != 1 {
		t.Fatalf("unexpected page: %+v", got)
	}
	got, err = client.List(context.Background(), ListOptions{Query: "Fights Break Sphere", Limit: 1})
	if err != nil || got.Items[0].State != "downloading" || got.Items[0].Progress != .5 || got.Items[0].DLSpeed != 1024 || got.Items[0].SavePath != "/data" {
		t.Fatalf("lost live status: %+v, %v", got, err)
	}
	got, err = client.List(context.Background(), ListOptions{Offset: 999})
	if err != nil || got.Count != 0 || got.Items == nil || got.HasMore || got.Total != 4 || got.Limit != 20 {
		t.Fatalf("unexpected empty page: %+v, %v", got, err)
	}
}

func TestListCompletedAndHash(t *testing.T) {
	hash := strings.Repeat("a", 40)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "completed" || r.URL.Query().Get("hashes") != hash {
			t.Errorf("filters not forwarded: %s", r.URL)
		}
		fmt.Fprint(w, `[{"hash":"`+hash+`","name":"Completed","state":"stoppedUP","progress":1,"completed":100,"amount_left":0,"completion_on":1000}]`)
	})
	got, err := client.List(context.Background(), ListOptions{Filter: "completed", Hash: strings.ToUpper(hash)})
	if err != nil || got.Total != 1 || got.Items[0].CompletedBytes != 100 || got.Items[0].CompletionOn != 1000 {
		t.Fatalf("unexpected completed result: %+v, %v", got, err)
	}
}

func TestListRejectsInvalidOptions(t *testing.T) {
	// Invalid queries must fail before trying to authenticate.
	client := New("http://127.0.0.1:1", "", "")
	for _, opts := range []ListOptions{{Filter: "bogus"}, {Limit: -1}, {Limit: 101}, {Offset: -1}, {Hash: "all"}, {Hash: strings.Repeat("z", 40)}} {
		if _, err := client.List(context.Background(), opts); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("expected validation error for %+v, got %v", opts, err)
		}
	}
}

func TestListErrorsDoNotLookLikeEmptyResults(t *testing.T) {
	for _, body := range []string{"<html>proxy error</html>", "null", `[{"name":"truncated"}`, "server error"} {
		t.Run(body, func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if body == "server error" {
					w.WriteHeader(http.StatusBadGateway)
				}
				fmt.Fprint(w, body)
			})
			if _, err := client.List(context.Background(), ListOptions{}); err == nil {
				t.Fatal("expected an error instead of an empty list")
			}
			if calls.Load() != 1 {
				t.Fatal("non-authentication errors should not retry")
			}
		})
	}
}

func TestListRenewsExpiredSession(t *testing.T) {
	var logins, reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "renewed"})
			fmt.Fprint(w, "Ok.")
			return
		}
		reads.Add(1)
		if cookie, _ := r.Cookie("SID"); cookie == nil || cookie.Value != "renewed" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()
	client := New(srv.URL, "", "")
	client.sid = "expired"
	got, err := client.List(context.Background(), ListOptions{})
	if err != nil || got.Items == nil || logins.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("session renewal failed: %+v, %v (logins=%d reads=%d)", got, err, logins.Load(), reads.Load())
	}
}

func TestFindExistingChecksAllTasks(t *testing.T) {
	hash := strings.Repeat("a", 40)
	items := make([]Torrent, 30)
	items[29] = Torrent{Hash: hash, Name: "Renamed by the user", State: "missingFiles", Progress: 1}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "all" || r.URL.Query().Has("limit") {
			t.Error("duplicate check must include all states and pages")
		}
		json.NewEncoder(w).Encode(items)
	})
	got, match, err := client.FindExisting(context.Background(), model.Torrent{Magnet: "magnet:?xt=urn:btih:" + strings.ToUpper(hash)})
	if err != nil || got == nil || got.Hash != hash || got.State != "missingFiles" || match != "hash" {
		t.Fatalf("renamed task not found: %+v, %s, %v", got, match, err)
	}
	base32Hash := base32.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 20)))
	if got := magnetHash("magnet:?xt=urn:btih:" + strings.ToLower(base32Hash)); got != strings.Repeat("61", 20) {
		t.Fatalf("base32 hash not recognized: %s", got)
	}
}

func TestFindExistingMatchesReleaseAndSize(t *testing.T) {
	resource := model.Torrent{Title: "Fights Break Sphere S05E219 2160p", SizeBytes: 1000}
	for _, tc := range []struct {
		name string
		size int64
		want bool
	}{
		{"Fights.Break.Sphere.S05E219.2160p.mkv", 1005, true},
		{"Fights Break Sphere S05E218 2160p", 1000, false},
		{"Fights Break Sphere S05E219 1080p", 1000, false},
		{resource.Title, 4000, false},
		{resource.Title, 0, false},
	} {
		t.Run(fmt.Sprint(tc.name, tc.size), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]Torrent{{Name: tc.name, TotalSize: tc.size, Size: 1}})
			})
			got, match, err := client.FindExisting(context.Background(), resource)
			if err != nil || (got != nil) != tc.want || (tc.want && match != "name_and_size") {
				t.Fatalf("unexpected match: %+v, %s, %v", got, match, err)
			}
		})
	}
}

func TestAddFilePreservesMultipartSubmission(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/torrents/add" {
			t.Errorf("unexpected submission: %s %s", r.Method, r.URL)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("torrents")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "release.torrent" || string(data) != "torrent bytes" || r.FormValue("savepath") != "/media" {
			t.Error("multipart submission was changed")
		}
		fmt.Fprint(w, "Ok.")
	})
	if err := client.AddFile(context.Background(), []byte("torrent bytes"), "release.torrent", "/media"); err != nil {
		t.Fatal(err)
	}
}

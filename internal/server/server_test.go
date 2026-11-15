package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHistoryUnavailable(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.history(w, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 when history is unavailable; got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == "" {
		t.Fatal("expected an error explaining that history is unavailable")
	}
}

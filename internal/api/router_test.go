package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouterHTTPContract(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, test := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"health", http.MethodGet, "/healthz", http.StatusOK},
		{"unknown route", http.MethodGet, "/unknown", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatal("HTTP responses must use JSON")
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if test.status == http.StatusOK {
				if body["status"] != "ok" || body["service"] != "sentinel-api" {
					t.Fatalf("unexpected health response: %v", body)
				}
			} else if body["error"] == "" {
				t.Fatal("error responses must include an error message")
			}
		})
	}
}

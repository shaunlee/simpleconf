package actions

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	app := New()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status mismatch: got %d", resp.StatusCode)
	}

	resp, err = app.Test(httptest.NewRequest(http.MethodPut, "/db/feature.flag", strings.NewReader("true")))
	if err != nil {
		t.Fatalf("PUT /db/feature.flag failed: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("PUT /db/feature.flag status mismatch: got %d", resp.StatusCode)
	}

	resp, err = app.Test(httptest.NewRequest(http.MethodGet, "/db/feature.flag", nil))
	if err != nil {
		t.Fatalf("GET /db/feature.flag failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := strings.TrimSpace(string(body)); got != "true" {
		t.Fatalf("GET /db/feature.flag body mismatch: got %q", got)
	}

	resp, err = app.Test(httptest.NewRequest(http.MethodPost, "/clone/feature.flag/feature.copy", nil))
	if err != nil {
		t.Fatalf("POST /clone failed: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /clone status mismatch: got %d", resp.StatusCode)
	}

	resp, err = app.Test(httptest.NewRequest(http.MethodGet, "/db/feature.copy", nil))
	if err != nil {
		t.Fatalf("GET /db/feature.copy failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := strings.TrimSpace(string(body)); got != "true" {
		t.Fatalf("GET /db/feature.copy body mismatch: got %q", got)
	}

	resp, err = app.Test(httptest.NewRequest(http.MethodDelete, "/db/feature.flag", nil))
	if err != nil {
		t.Fatalf("DELETE /db/feature.flag failed: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("DELETE /db/feature.flag status mismatch: got %d", resp.StatusCode)
	}
}

func TestUpdateInvalidJSON(t *testing.T) {
	app := New()
	resp, err := app.Test(httptest.NewRequest(http.MethodPut, "/db/bad", strings.NewReader("{")))
	if err != nil {
		t.Fatalf("PUT /db/bad failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid json status mismatch: got %d", resp.StatusCode)
	}
}

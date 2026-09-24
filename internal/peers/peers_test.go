package peers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shaunlee/simpleconf/internal/db"
)

func useDB(t *testing.T) {
	t.Helper()
	db.Init(t.TempDir())
	t.Cleanup(func() { db.Close() })
	// Init loads into the document left by an earlier test.
	if err := db.Replace("{}"); err != nil {
		t.Fatal(err)
	}
}

func TestPeerRoutes(t *testing.T) {
	useDB(t)
	app := newApp()

	steps := []struct {
		method, path, body string
		status             int
		key, want          string
	}{
		{http.MethodPut, "/db/a", `{"b":1}`, http.StatusAccepted, "a.b", "1"},
		{http.MethodPut, "/db/a", `{`, http.StatusUnprocessableEntity, "a.b", "1"},
		{http.MethodPut, "/db/big", `9007199254740993`, http.StatusAccepted, "big", "9007199254740993"},
		{http.MethodPut, "/db/arr", `[1]`, http.StatusAccepted, "arr", "[1]"},
		{http.MethodPut, "/db/arr.x", `2`, http.StatusBadRequest, "arr", "[1]"},
		{http.MethodPost, "/clone/a/c", "", http.StatusAccepted, "c.b", "1"},
		{http.MethodDelete, "/db/a", "", http.StatusAccepted, "a", ""},
		{http.MethodPost, "/vacuum", "", http.StatusAccepted, "c.b", "1"},
	}
	for _, s := range steps {
		resp, err := app.Test(httptest.NewRequest(s.method, s.path, strings.NewReader(s.body)))
		if err != nil {
			t.Fatalf("%s %s: %v", s.method, s.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != s.status {
			t.Fatalf("%s %s: status %d want %d", s.method, s.path, resp.StatusCode, s.status)
		}
		if got := db.Get(s.key); got != s.want {
			t.Fatalf("after %s %s: Get(%q) = %q want %q", s.method, s.path, s.key, got, s.want)
		}
	}

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/db", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got, want := resp.Header.Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("GET /db content type %q want %q", got, want)
	}
	if got, want := string(body), db.Get(""); got != want {
		t.Fatalf("GET /db = %q want %q", got, want)
	}
}

package httpapi

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/shaunlee/simpleconf/internal/cluster"
	"github.com/shaunlee/simpleconf/internal/db"
)

func call(t *testing.T, app *fiber.App, method, path, body string) (int, string, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, strings.NewReader(body)))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestWholeAndVacuum(t *testing.T) {
	db.Init(t.TempDir())
	defer db.Close()
	app := New()

	if status, _, _ := call(t, app, http.MethodPut, "/db/w", `{"x":1}`); status != http.StatusAccepted {
		t.Fatalf("PUT status %d", status)
	}
	status, ctype, body := call(t, app, http.MethodGet, "/db", "")
	if status != http.StatusOK || ctype != "application/json" || body != db.Get("") {
		t.Fatalf("GET /db = %d %q %q, want 200 application/json %q", status, ctype, body, db.Get(""))
	}
	if status, _, _ := call(t, app, http.MethodPost, "/vacuum", ""); status != http.StatusAccepted {
		t.Fatalf("POST /vacuum status %d", status)
	}
}

func TestUpdateBadPath(t *testing.T) {
	db.Init(t.TempDir())
	defer db.Close()
	app := New()

	if status, _, _ := call(t, app, http.MethodPut, "/db/arr", `[1]`); status != http.StatusAccepted {
		t.Fatalf("PUT /db/arr status %d", status)
	}
	if status, _, body := call(t, app, http.MethodPut, "/db/arr.x", `2`); status != http.StatusBadRequest || !strings.Contains(body, "non-numeric") {
		t.Fatalf("PUT /db/arr.x = %d %s, want 400 path error", status, body)
	}
}

// A Raft node that was never bootstrapped has no leader, so every write is refused.
func TestWritesWithoutLeader(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	m, err := cluster.Start(cluster.Config{Enabled: true, NodeID: "n1", RaftAddr: addr, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	cluster.SetDefault(m)
	defer cluster.SetDefault(&cluster.Manager{})

	app := New()
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPut, "/db/k", `1`},
		{http.MethodDelete, "/db/k", ""},
		{http.MethodPost, "/clone/a/b", ""},
		{http.MethodPost, "/vacuum", ""},
	} {
		status, _, body := call(t, app, r.method, r.path, r.body)
		if want := `{"error":"not leader","leader":""}`; status != http.StatusConflict || body != want {
			t.Fatalf("%s %s = %d %s, want 409 %s", r.method, r.path, status, body, want)
		}
	}
}

func TestReply(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"ok", nil, http.StatusAccepted, `{"ok":true}`},
		{"bad json", &db.JSONError{Err: errors.New("unexpected end")}, http.StatusUnprocessableEntity, `{"error":"unexpected end"}`},
		{"not leader", &cluster.NotLeaderError{LeaderHTTPAddr: "http://10.0.0.1:23456"}, http.StatusConflict, `{"error":"not leader","leader":"http://10.0.0.1:23456"}`},
		{"other", errors.New("boom"), http.StatusBadRequest, `{"error":"boom"}`},
		{"writes refused", db.ErrWritesRefused, http.StatusServiceUnavailable, `{"error":"writes refused: the append-only file cannot be written"}`},
	}
	for _, c := range cases {
		app := fiber.New()
		app.Get("/", func(ctx fiber.Ctx) error { return reply(ctx, c.err) })
		status, _, body := call(t, app, http.MethodGet, "/", "")
		if status != c.status || body != c.body {
			t.Fatalf("%s: got %d %s want %d %s", c.name, status, body, c.status, c.body)
		}
	}
}

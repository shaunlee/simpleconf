package cluster

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type forwarded struct {
	method, path, contentType, body string
}

// fakeLeader answers every request with status and body, recording what it got.
func fakeLeader(t *testing.T, status int, body string) (*httptest.Server, *forwarded) {
	t.Helper()
	got := &forwarded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = forwarded{r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(b)}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestForwardToLeaderRequests(t *testing.T) {
	cases := []struct {
		c    command
		want forwarded
	}{
		{command{Op: "set", Key: "a.b", Value: map[string]any{"n": 1}}, forwarded{http.MethodPut, "/db/a.b", "application/json", `{"n":1}`}},
		{command{Op: "del", Key: "a.b"}, forwarded{http.MethodDelete, "/db/a.b", "", ""}},
		{command{Op: "clone", FromKey: "a", ToKey: "b"}, forwarded{http.MethodPost, "/clone/a/b", "", ""}},
		{command{Op: "vacuum"}, forwarded{http.MethodPost, "/vacuum", "", ""}},
	}
	m := &Manager{forward: true}
	for _, c := range cases {
		srv, got := fakeLeader(t, http.StatusNoContent, "")
		if err := m.forwardToLeader(c.c, srv.URL); err != nil {
			t.Fatalf("%s: %v", c.c.Op, err)
		}
		if *got != c.want {
			t.Fatalf("%s: leader got %+v want %+v", c.c.Op, *got, c.want)
		}
	}
}

func TestForwardToLeaderResponses(t *testing.T) {
	m := &Manager{forward: true}
	del := command{Op: "del", Key: "k"}

	srv, _ := fakeLeader(t, http.StatusConflict, `{"leader":"http://other:1"}`)
	if nl, ok := AsNotLeader(m.forwardToLeader(del, srv.URL)); !ok || nl.LeaderHTTPAddr != "http://other:1" {
		t.Fatalf("409 with leader: got %+v, %v", nl, ok)
	}

	srv, _ = fakeLeader(t, http.StatusConflict, "not json")
	if nl, ok := AsNotLeader(m.forwardToLeader(del, srv.URL)); !ok || nl.LeaderHTTPAddr != srv.URL {
		t.Fatalf("409 without leader: got %+v, %v", nl, ok)
	}

	srv, _ = fakeLeader(t, http.StatusInternalServerError, "boom")
	if err := m.forwardToLeader(del, srv.URL); err == nil || !strings.Contains(err.Error(), "status=500 body=boom") {
		t.Fatalf("500 error = %v", err)
	}
}

func TestForwardToLeaderErrors(t *testing.T) {
	m := &Manager{forward: true}

	if err := m.forwardToLeader(command{Op: "bogus"}, "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("unknown op error = %v", err)
	}
	if err := m.forwardToLeader(command{Op: "set", Key: "k", Value: make(chan int)}, "http://127.0.0.1:1"); err == nil {
		t.Fatal("unencodable value should fail")
	}
	if err := m.forwardToLeader(command{Op: "vacuum"}, "http://[::1"); err == nil {
		t.Fatal("malformed leader URL should fail")
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if err := m.forwardToLeader(command{Op: "vacuum"}, url); err == nil {
		t.Fatal("unreachable leader should fail")
	}
}

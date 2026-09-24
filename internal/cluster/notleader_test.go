package cluster

import (
	"errors"
	"fmt"
	"testing"
)

func TestNotLeaderError(t *testing.T) {
	if got, want := (&NotLeaderError{}).Error(), "not leader"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := (&NotLeaderError{LeaderHTTPAddr: "http://a:1"}).Error(), "not leader, leader=http://a:1"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAsNotLeader(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		ok     bool
		leader string
	}{
		{"nil", nil, false, ""},
		{"other", errors.New("boom"), false, ""},
		{"direct", &NotLeaderError{LeaderHTTPAddr: "http://a:1"}, true, "http://a:1"},
		{"wrapped", fmt.Errorf("apply: %w", &NotLeaderError{LeaderHTTPAddr: "http://b:2"}), true, "http://b:2"},
	}
	for _, c := range cases {
		nl, ok := AsNotLeader(c.err)
		if ok != c.ok {
			t.Fatalf("%s: ok = %v want %v", c.name, ok, c.ok)
		}
		if ok && nl.LeaderHTTPAddr != c.leader {
			t.Fatalf("%s: leader = %q want %q", c.name, nl.LeaderHTTPAddr, c.leader)
		}
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, yml string) *Config {
	t.Helper()
	dir := t.TempDir()
	if yml != "" {
		os.Mkdir(filepath.Join(dir, "configs"), 0o755)
		os.WriteFile(filepath.Join(dir, "configs", "config.yml"), []byte(yml), 0o644)
	}
	t.Chdir(dir)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaultsWithoutFile(t *testing.T) {
	c := load(t, "")
	if c.Listen != ":23456" || c.DBDir != "/data" || !c.Raft.Forward || c.TCPListen != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestKeyPrecedence(t *testing.T) {
	c := load(t, "db_dir: flat\ndb:\n  dir: nested\ntcp_listen: :1\ntcp:\n  listen: :2\npeers_listen: :3\npeers:\n  listen: :4\n")
	if c.DBDir != "flat" || c.TCPListen != ":1" || c.PeerListen != ":4" {
		t.Fatalf("got db=%q tcp=%q peers=%q", c.DBDir, c.TCPListen, c.PeerListen)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv("DB_DIR", "fromenv")
	c := load(t, "db:\n  dir: nested\n")
	if c.DBDir != "fromenv" {
		t.Fatalf("got %q", c.DBDir)
	}
}

func TestRaftPeers(t *testing.T) {
	c := load(t, "raft:\n  peers:\n    - \"n1, 127.0.0.1:1, 127.0.0.1:2\"\n    - bad\n")
	if len(c.Raft.Peers) != 1 || c.Raft.Peers[0].ID != "n1" || c.Raft.Peers[0].HTTPAddr != "127.0.0.1:2" {
		t.Fatalf("got %+v", c.Raft.Peers)
	}
}

func TestRaftFsync(t *testing.T) {
	if c := load(t, ""); c.Raft.Fsync != "" {
		t.Fatalf("default raft.fsync = %q, want empty (always)", c.Raft.Fsync)
	}
	if c := load(t, "raft:\n  fsync: everysec\n"); c.Raft.Fsync != "everysec" {
		t.Fatalf("raft.fsync = %q", c.Raft.Fsync)
	}
}

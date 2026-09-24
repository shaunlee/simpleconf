package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFsyncPolicy(t *testing.T) {
	cases := map[string]FsyncPolicy{"": FsyncEverysec, "everysec": FsyncEverysec, "always": FsyncAlways, "no": FsyncNo}
	for in, want := range cases {
		if got, err := ParseFsyncPolicy(in); err != nil || got != want {
			t.Fatalf("ParseFsyncPolicy(%q) = %v, %v want %v", in, got, err, want)
		}
	}
	if _, err := ParseFsyncPolicy("sometimes"); err == nil {
		t.Fatal("an unknown policy should fail")
	}
}

func withFsyncPolicy(t *testing.T, p FsyncPolicy) {
	t.Helper()
	prev := fsyncPolicy
	SetFsyncPolicy(p)
	t.Cleanup(func() { SetFsyncPolicy(prev) })
}

func readAOF(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "data.aof"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Under FsyncAlways a write is on disk by the time Set returns.
func TestFsyncAlways(t *testing.T) {
	withFsyncPolicy(t, FsyncAlways)
	dir := useAOF(t)
	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if got, want := readAOF(t, dir), "+k\n\"v\"\n"; got != want {
		t.Fatalf("AOF right after Set = %q want %q", got, want)
	}
}

func TestFsyncNo(t *testing.T) {
	withFsyncPolicy(t, FsyncNo)
	dir := useAOF(t)
	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	Del("k")
	Close()
	if got, want := readAOF(t, dir), "+k\n\"v\"\n-k\n"; got != want {
		t.Fatalf("AOF = %q want %q", got, want)
	}
}

func TestDisableAOF(t *testing.T) {
	DisableAOF()
	t.Cleanup(func() { aofEnabled = true })
	dir := useAOF(t)
	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	Vacuum()
	Close()
	if Get("k") != `"v"` {
		t.Fatalf("in-memory write lost: %s", Get(""))
	}
	if _, err := os.Stat(filepath.Join(dir, "data.aof")); !os.IsNotExist(err) {
		t.Fatalf("AOF file should not exist, stat err = %v", err)
	}
}

func TestReplayMalformedAOF(t *testing.T) {
	cases := []struct {
		name, aof, want string
	}{
		{"bad record is skipped", "+arr\n[1]\n+arr.x\n2\n+k\n\"v\"\n", `{"arr":[1],"k":"v"}`},
		{"unknown record is ignored", "?\n+k\n1\n", `{"k":1}`},
		{"delete of a missing key", "-gone\n+k\n1\n", `{"k":1}`},
		{"truncated set", "+k\n1\n+cut\n", `{"k":1}`},
		{"truncated dump", "+k\n1\n*\n", `{"k":1}`},
		{"dump that is not an object", "+old\n1\n*\n[1]\n+k\n1\n", `{"k":1}`},
		{"missing final newline", "+k\n1\n+tail\n2", `{"k":1}`},
	}
	for _, c := range cases {
		resetConfig()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "data.aof"), []byte(c.aof), 0o600); err != nil {
			t.Fatal(err)
		}
		Init(dir)
		got := Get("")
		Close()
		if got != c.want {
			t.Fatalf("%s: replayed %s want %s", c.name, got, c.want)
		}
	}
}

func TestVacuumRewritesAOF(t *testing.T) {
	dir := useAOF(t)
	for i := 0; i < 3; i++ {
		if err := Set("k", i); err != nil {
			t.Fatal(err)
		}
	}
	Vacuum()
	Close()
	if got, want := readAOF(t, dir), "*\n{\"k\":2}\n"; got != want {
		t.Fatalf("AOF after vacuum = %q want %q", got, want)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "data.aof.*"))
	if len(backups) != 1 || !strings.Contains(readFile(t, backups[0]), "+k\n0\n") {
		t.Fatalf("vacuum should keep the old AOF as one backup, got %v", backups)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A vacuum that cannot write its snapshot must leave the AOF, and appends to
// it, intact.
func TestVacuumWriteFailureKeepsAOF(t *testing.T) {
	dir := useAOF(t)
	if err := Set("k", 1); err != nil {
		t.Fatal(err)
	}
	// A directory where the temp file should go makes the snapshot write fail.
	if err := os.Mkdir(filepath.Join(dir, "data.aof.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	Vacuum()
	if err := Set("after", 2); err != nil {
		t.Fatal(err)
	}
	Close()

	if got, want := readAOF(t, dir), "+k\n1\n+after\n2\n"; got != want {
		t.Fatalf("AOF after a failed vacuum = %q want %q", got, want)
	}
	if backups, _ := filepath.Glob(filepath.Join(dir, "data.aof.[0-9]*")); len(backups) != 0 {
		t.Fatalf("a failed vacuum should not rotate the AOF, got backups %v", backups)
	}

	resetConfig()
	Init(dir)
	defer Close()
	if got, want := Get(""), `{"k":1,"after":2}`; got != want {
		t.Fatalf("reloaded %s want %s", got, want)
	}
}

func TestVacuumTwiceInOneSecond(t *testing.T) {
	dir := useAOF(t)
	for i := 0; i < 2; i++ {
		if err := Set("k", i); err != nil {
			t.Fatal(err)
		}
		Vacuum()
	}
	Close()
	if got, want := readAOF(t, dir), "*\n{\"k\":1}\n"; got != want {
		t.Fatalf("AOF = %q want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "data.aof.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

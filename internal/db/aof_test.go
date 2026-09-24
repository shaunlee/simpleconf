package db

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestInitCreatesDir(t *testing.T) {
	resetConfig()
	dir := filepath.Join(t.TempDir(), "not", "yet")
	Init(dir)
	defer Close()
	if err := Set("k", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data.aof")); err != nil {
		t.Fatalf("AOF not created in a new directory: %v", err)
	}
}

// A crash in the middle of a write leaves the AOF ending in part of a record.
// It is cut off on load, so the next write is stored under its own key and
// survives the next restart.
func TestAOFTornTail(t *testing.T) {
	cases := map[string]string{
		"key line cut":       "+a\n1\n+ha",
		"value line missing": "+a\n1\n+half\n",
		"value line cut":     "+a\n1\n+half\n12",
		"dump cut":           "+a\n1\n*\n{\"a\"",
		"delete cut":         "+a\n1\n-go",
		"blank line":         "\n+a\n1\n",
	}
	for name, aof := range cases {
		resetConfig()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "data.aof"), []byte(aof), 0o600); err != nil {
			t.Fatal(err)
		}
		Init(dir)
		if err := Set("new", 3); err != nil {
			t.Fatal(err)
		}
		Close()

		resetConfig()
		Init(dir)
		got := Get("")
		Close()
		if want := `{"a":1,"new":3}`; got != want {
			t.Fatalf("%s: reloaded %s want %s (aof %q)", name, got, want, readAOF(t, dir))
		}
	}
}

// fakeClock makes every vacuum see a time one second after the last, so each
// backup gets its own name.
func fakeClock(t *testing.T) {
	t.Helper()
	var tick atomic.Int64
	start := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	prev := now
	now = func() time.Time { return start.Add(time.Duration(tick.Add(1)) * time.Second) }
	t.Cleanup(func() { now = prev })
}

func withBackups(t *testing.T, n int) {
	t.Helper()
	prev := backups
	SetBackups(n)
	t.Cleanup(func() { SetBackups(prev) })
}

func backupNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "data.aof.") {
			names = append(names, e.Name())
		}
	}
	return names
}

// vacuumTimes writes and vacuums n times, then closes the db.
func vacuumTimes(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := Set("k", i); err != nil {
			t.Fatal(err)
		}
		Vacuum()
	}
	Close()
}

func TestVacuumKeepsNewestBackups(t *testing.T) {
	cases := []struct {
		keep int
		want []string
	}{
		{2, []string{"data.aof.260102030404", "data.aof.260102030405"}},
		{0, nil},
		{-1, []string{"data.aof.260102030401", "data.aof.260102030402", "data.aof.260102030403", "data.aof.260102030404", "data.aof.260102030405"}},
	}
	for _, c := range cases {
		fakeClock(t)
		withBackups(t, c.keep)
		dir := useAOF(t)
		vacuumTimes(t, 5)
		if got := backupNames(t, dir); !slices.Equal(got, c.want) {
			t.Fatalf("keep %d: backups %v want %v", c.keep, got, c.want)
		}
		if got := readAOF(t, dir); got != "*\n{\"k\":4}\n" {
			t.Fatalf("keep %d: AOF = %q", c.keep, got)
		}
	}
}

// Pruning touches only files named like a backup, and a file it cannot
// remove does not stop the vacuum.
func TestVacuumPruneLeavesOtherFiles(t *testing.T) {
	fakeClock(t)
	withBackups(t, 0)
	dir := useAOF(t)
	others := []string{"data.aof.bak", "data.aof.2601", "data.aof.2601020304000", "data.aof.26010203040x", "other.260102030400"}
	for _, n := range others {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A backup name that cannot be removed: a directory with a file in it.
	stuck := filepath.Join(dir, "data.aof.200101000000")
	if err := os.MkdirAll(filepath.Join(stuck, "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	vacuumTimes(t, 1)

	for _, n := range append(others, "data.aof.200101000000") {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s should still exist: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "data.aof.260102030401")); !os.IsNotExist(err) {
		t.Fatalf("the new backup should have been pruned, stat err = %v", err)
	}
	if got := readAOF(t, dir); got != "*\n{\"k\":0}\n" {
		t.Fatalf("AOF = %q", got)
	}
}

func TestIsBackupName(t *testing.T) {
	for name, want := range map[string]bool{
		"data.aof.260102030405":  true,
		"data.aof.26010203040":   false,
		"data.aof.2601020304050": false,
		"data.aof.26010203040a":  false,
		"data.aof.tmp":           false,
		"data.aof":               false,
		"xdata.aof.260102030405": false,
	} {
		if got := isBackupName("data.aof", name); got != want {
			t.Fatalf("isBackupName(%q) = %v want %v", name, got, want)
		}
	}
}

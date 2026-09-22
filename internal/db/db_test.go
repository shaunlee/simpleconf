package db

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

func resetConfig() {
	configMu.Lock()
	configRoot = newTreeObj()
	configSnapshot = "{}"
	configStale = false
	configMu.Unlock()
}

func TestSetGetDelClone(t *testing.T) {
	resetConfig()

	if err := setonly("bench", "mark"); err != nil {
		t.Fatalf("setonly failed: %v", err)
	}
	if got, want := Get("bench"), "\"mark\""; got != want {
		t.Fatalf("Get(bench) mismatch: got %q want %q", got, want)
	}
	if got := Get(""); !strings.Contains(got, "\"bench\":\"mark\"") {
		t.Fatalf("Get(\"\") should contain bench field, got %q", got)
	}

	Clone("bench", "bench_copy")
	if got, want := Get("bench_copy"), "\"mark\""; got != want {
		t.Fatalf("Clone result mismatch: got %q want %q", got, want)
	}
	if got, want := cloneonly("bench", "bench_copy2"), "\"mark\""; got != want {
		t.Fatalf("cloneonly should report the copied value: got %q want %q", got, want)
	}

	Clone("missing", "ignored")
	if got := Get("ignored"); got != "" {
		t.Fatalf("missing source clone should not write target, got %q", got)
	}

	delonly("bench")
	if got := Get("bench"); got != "" {
		t.Fatalf("delonly should delete value, got %q", got)
	}
}

func TestReadline(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("one\n"))
	if got := readline(r); string(got) != "one" {
		t.Fatalf("readline mismatch: got %q", string(got))
	}

	r = bufio.NewReader(strings.NewReader(""))
	if got := readline(r); got != nil {
		t.Fatalf("readline on EOF should return nil, got %q", string(got))
	}
}

func TestInitReloadAndClose(t *testing.T) {
	resetConfig()
	tmp := t.TempDir()

	Init(tmp)
	defer Close()

	if err := Set("a", "b"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	Del("a")
	if err := Set("x", map[string]any{"y": 1}); err != nil {
		t.Fatalf("Set object failed: %v", err)
	}
	wg.Wait()
	Close()

	resetConfig()
	Init(tmp)
	defer Close()
	if got, want := Get("x.y"), "1"; got != want {
		t.Fatalf("reloaded value mismatch: got %q want %q", got, want)
	}
	if got := Get("a"); got != "" {
		t.Fatalf("deleted key should stay deleted after reload, got %q", got)
	}

	Close(true)
	if _, err := os.Stat(dbfn); err != nil {
		t.Fatalf("db file should exist after Close(true): %v", err)
	}
}

func TestKeySurvivesCallerBufferReuse(t *testing.T) {
	resetConfig()
	buf := []byte("n3")
	k := unsafe.String(unsafe.SliceData(buf), len(buf))
	if err := setonly(k, "mark"); err != nil {
		t.Fatal(err)
	}
	copy(buf, "zz")
	if got, want := Get("n3"), `"mark"`; got != want {
		t.Fatalf("Get(n3) = %q, want %q; whole %s", got, want, Get(""))
	}
}

func TestNumberAndHTMLReload(t *testing.T) {
	resetConfig()
	tmp := t.TempDir()
	Init(tmp)
	defer Close()

	if err := SetRaw("n2", []byte(`1e21`)); err != nil {
		t.Fatal(err)
	}
	if err := SetRaw("n4", []byte(`123456789012345678901234567890`)); err != nil {
		t.Fatal(err)
	}
	if err := SetRaw("n7", []byte(`-0.0`)); err != nil {
		t.Fatal(err)
	}
	if err := SetRaw("h", []byte(`"a<b>c&d"`)); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"n2": Get("n2"),
		"n4": Get("n4"),
		"n7": Get("n7"),
		"h":  Get("h"),
	}
	if want["h"] != `"a<b>c&d"` {
		t.Fatalf("live h = %q", want["h"])
	}
	if want["n7"] != `-0` {
		t.Fatalf("live n7 = %q", want["n7"])
	}
	wg.Wait()
	Close()

	raw, err := os.ReadFile(filepath.Join(tmp, "data.aof"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u003c`) {
		t.Fatalf("aof html-escaped the string:\n%s", raw)
	}
	if strings.Contains(string(raw), "1e+21") || strings.Contains(string(raw), "1e21") {
		t.Fatalf("aof kept scientific notation:\n%s", raw)
	}

	resetConfig()
	Init(tmp)
	defer Close()
	for k, v := range want {
		if got := Get(k); got != v {
			t.Fatalf("reload %s = %q, want %q", k, got, v)
		}
	}
}

func TestAppendPersists(t *testing.T) {
	resetConfig()
	tmp := t.TempDir()
	Init(tmp)
	defer Close()
	if err := SetRaw("arr", []byte(`[1]`)); err != nil {
		t.Fatal(err)
	}
	if err := SetRaw("arr.-1", []byte(`123`)); err != nil {
		t.Fatal(err)
	}
	if got, want := Get("arr"), `[1,123]`; got != want {
		t.Fatalf("live arr = %q, want %q", got, want)
	}
	wg.Wait()
	Close()
	raw, err := os.ReadFile(filepath.Join(tmp, "data.aof"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "+arr.-1\n123\n") {
		t.Fatalf("aof missing append value:\n%s", raw)
	}
	resetConfig()
	Init(tmp)
	defer Close()
	if got, want := Get("arr"), `[1,123]`; got != want {
		t.Fatalf("reload arr = %q, want %q", got, want)
	}
}

func TestConcurrentSetLog(t *testing.T) {
	resetConfig()
	tmp := t.TempDir()
	Init(tmp)
	defer Close()

	const n = 200
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := Set("obj", map[string]any{"n": i, "m": i}); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := Set("obj.n", i); err != nil {
				errc <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}

	// Each Set("obj") logged one object. Its two fields were written together,
	// so a torn walk would show up here as m != n.
	Close()
	raw, err := os.ReadFile(filepath.Join(tmp, "data.aof"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	sawObj := false
	for i := 0; i < len(lines); i++ {
		if lines[i] != "+obj" {
			continue
		}
		if i+1 >= len(lines) {
			t.Fatalf("truncated object record:\n%s", raw)
		}
		val := lines[i+1]
		var m, n int
		if _, err := fmt.Sscanf(val, `{"m":%d,"n":%d}`, &m, &n); err != nil || m != n || val != fmt.Sprintf(`{"m":%d,"n":%d}`, m, n) {
			t.Fatalf("torn object log: %s", val)
		}
		sawObj = true
		i++
	}
	if !sawObj {
		t.Fatalf("aof has no object record:\n%s", raw)
	}
}

func TestBigIntegerKept(t *testing.T) {
	const n = "9007199254740993"
	resetConfig()
	tmp := t.TempDir()
	Init(tmp)
	defer Close()
	if err := SetRaw("n", []byte(n)); err != nil {
		t.Fatal(err)
	}
	if err := SetRaw("o", []byte(`{"n":`+n+`}`)); err != nil {
		t.Fatal(err)
	}
	if got := Get("n"); got != n {
		t.Fatalf("live n = %q", got)
	}
	if got := Get("o.n"); got != n {
		t.Fatalf("live o.n = %q", got)
	}
	wg.Wait()
	Close()

	resetConfig()
	body := "+n\n" + n + "\n+o\n{\"n\":" + n + "}\n"
	if err := os.WriteFile(filepath.Join(tmp, "data.aof"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	Init(tmp)
	defer Close()
	if got := Get("n"); got != n {
		t.Fatalf("replay n = %q", got)
	}
	if got := Get("o.n"); got != n {
		t.Fatalf("replay o.n = %q", got)
	}
}

func TestOldAOFNumberReplay(t *testing.T) {
	resetConfig()
	tmp := t.TempDir()
	body := "+n2\n1e+21\n+n4\n1.2345678901234568e+29\n+n7\n-0\n+h\n\"a\\u003cb\\u003ec\\u0026d\"\n+raw\n\"a<b>c&d\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "data.aof"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	Init(tmp)
	defer Close()
	if got, want := Get("n2"), "1000000000000000000000"; got != want {
		t.Fatalf("n2 = %q, want %q", got, want)
	}
	if got, want := Get("n4"), "123456789012345680000000000000"; got != want {
		t.Fatalf("n4 = %q, want %q", got, want)
	}
	if got, want := Get("n7"), "-0"; got != want {
		t.Fatalf("n7 = %q, want %q", got, want)
	}
	if got, want := Get("h"), `"a\u003cb\u003ec\u0026d"`; got != want {
		t.Fatalf("h = %q, want %q", got, want)
	}
	if got, want := Get("raw"), `"a<b>c&d"`; got != want {
		t.Fatalf("raw = %q, want %q", got, want)
	}
}

func BenchmarkGet(b *testing.B) {
	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		Get("bench")
	}
}

func BenchmarkSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		setonly("bench", "mark")
	}
}

func BenchmarkDel(b *testing.B) {
	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		delonly("bench")
	}
}

func BenchmarkClone(b *testing.B) {
	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		cloneonly("bench", "mark")
	}
}

// BenchmarkDocSize measures how an operation's cost varies with the size of
// the document and with where in it the key sits. The numbers quoted in
// README.md come from this benchmark.
func BenchmarkDocSize(b *testing.B) {
	for _, n := range []struct {
		name string
		keys int
	}{{"60B", 0}, {"600B", 6}, {"6KB", 70}, {"60KB", 750}} {
		b.Run(n.name, func(b *testing.B) {
			resetConfig()
			setonly("first", "mark")
			for i := 0; i < n.keys; i++ {
				setonly(fmt.Sprintf("svc%04d", i), map[string]any{
					"name": strings.Repeat("x", 40),
					"port": 8000,
				})
			}
			setonly("last", "mark")

			b.Run("Get_first", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					Get("first")
				}
			})
			b.Run("Get_last", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					Get("last")
				}
			})
			b.Run("Get_whole", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					Get("")
				}
			})
			b.Run("Set_first", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					setonly("first", "mark")
				}
			})
			b.Run("Set_last", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					setonly("last", "mark")
				}
			})
		})
	}
}

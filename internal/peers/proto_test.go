package peers

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/shaunlee/simpleconf/internal/db"
	"github.com/shaunlee/simpleconf/internal/wire"
)

// startPeer serves the peers port on a new address. HTTP requests fail the
// test unless allowHTTP, so a test can tell the protocol was used.
func startPeer(t *testing.T, allowHTTP bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux, app := newServer(ln)
	if allowHTTP {
		go func() { _ = app.Listener(mux, fiber.ListenConfig{DisableStartupMessage: true}) }()
		t.Cleanup(func() { _ = app.Shutdown() })
	} else {
		go func() {
			if c, err := mux.Accept(); err == nil {
				t.Error("the peer got an HTTP connection")
				_ = c.Close()
			}
		}()
	}
	t.Cleanup(func() { _ = mux.Close() })
	return "http://" + ln.Addr().String()
}

// drained waits until w has sent everything queued.
func drained(t *testing.T, w *workerState) {
	t.Helper()
	if !eventually(t, 3*time.Second, func() bool { _, ok := w.peek(); return !ok }) {
		t.Fatalf("queue for %s not drained: %v", w.addr, pendingPaths(w))
	}
}

func swapApplyOp(t *testing.T, fn func(op) error) {
	t.Helper()
	orig := applyOp
	applyOp = fn
	t.Cleanup(func() { applyOp = orig })
}

func TestOpRoundTrip(t *testing.T) {
	big := []byte(`{"n":9007199254740993}`)
	cases := []struct {
		queued syncOp
		want   op
	}{
		{syncOp{Method: "PUT", Path: "/db/a.b", Body: big}, op{kind: opSet, key: "a.b", raw: big}},
		{syncOp{Method: "DELETE", Path: "/db/a.b"}, op{kind: opDel, key: "a.b"}},
		{syncOp{Method: "POST", Path: "/clone/a/b"}, op{kind: opClone, key: "a", to: "b"}},
		{syncOp{Method: "POST", Path: "/vacuum"}, op{kind: opVacuum}},
	}
	var buf strings.Builder
	w := bufio.NewWriter(&buf)
	for _, c := range cases {
		o, err := toOp(c.queued)
		if err != nil {
			t.Fatalf("%+v: %v", c.queued, err)
		}
		if err := writeOp(w, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(strings.NewReader(buf.String()))
	for _, c := range cases {
		got, err := readOp(r)
		if err != nil {
			t.Fatal(err)
		}
		if got.kind != c.want.kind || got.key != c.want.key || got.to != c.want.to || string(got.raw) != string(c.want.raw) {
			t.Fatalf("read %+v want %+v", got, c.want)
		}
	}

	for _, bad := range []syncOp{{Method: "POST", Path: "/clone/a"}, {Method: "POST", Path: "/clone/a/b/c"}, {Method: "GET", Path: "/db"}} {
		if _, err := toOp(bad); err == nil {
			t.Fatalf("%+v should not convert", bad)
		}
	}
	if _, err := readOp(bufio.NewReader(strings.NewReader("x"))); err == nil {
		t.Fatal("an unknown op should fail")
	}
}

// A refused write stops the batch, so it and the rest are sent again; any
// other error drops that write alone.
func TestApplyBatchStopsWhenRefused(t *testing.T) {
	var calls []string
	swapApplyOp(t, func(o op) error {
		calls = append(calls, o.key)
		switch o.key {
		case "bad":
			return errors.New("invalid path")
		case "full":
			return db.ErrWritesRefused
		}
		return nil
	})
	res := applyBatch([]op{{kind: opDel, key: "a"}, {kind: opDel, key: "bad"}, {kind: opDel, key: "b"}, {kind: opDel, key: "full"}, {kind: opDel, key: "c"}})
	if res.done != 3 || !reflect.DeepEqual(res.rejected, map[int]string{1: "invalid path"}) || res.stopped == "" {
		t.Fatalf("got %+v", res)
	}
	if strings.Join(calls, ",") != "a,bad,b,full" {
		t.Fatalf("applied %v", calls)
	}
}

// Ops queued for a peer that speaks the peers protocol arrive in order, in
// batches, without HTTP, and a large integer keeps its digits.
func TestWorkerSendsOverPeersProtocol(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	addr := startPeer(t, false)
	w := workerFor(addr)

	var applied atomic.Int32
	orig := applyOp
	swapApplyOp(t, func(o op) error { applied.Add(1); return orig(o) })

	for _, s := range []syncOp{
		{Method: "PUT", Path: "/db/big", Body: []byte(`9007199254740993`)},
		{Method: "PUT", Path: "/db/a", Body: []byte(`{"x":1}`)},
		{Method: "POST", Path: "/clone/a/b"},
		{Method: "DELETE", Path: "/db/a"},
		{Method: "POST", Path: "/vacuum"},
	} {
		if _, err := w.enqueue(s); err != nil {
			t.Fatal(err)
		}
	}
	drained(t, w)
	if got := db.Get(""); got != `{"big":9007199254740993,"b":{"x":1}}` {
		t.Fatalf("peer document = %s", got)
	}
	if n := applied.Load(); n != 5 {
		t.Fatalf("%d ops applied, want 5", n)
	}
	if got := walPaths(t, w); len(got) != 0 {
		t.Fatalf("WAL still holds %v", got)
	}
}

// While the peer refuses writes the worker keeps retrying from the refused
// op; a rejected op is dropped and the rest still arrive.
func TestWorkerRetriesRefusedAndDropsRejected(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	fastRetry(t)
	addr := startPeer(t, false)

	var refusals atomic.Int32
	refusals.Store(3)
	var got []string
	swapApplyOp(t, func(o op) error {
		if o.key == "later" && refusals.Add(-1) >= 0 {
			return db.ErrWritesRefused
		}
		if o.key == "bad" {
			return errors.New("invalid path")
		}
		got = append(got, o.key)
		return nil
	})
	w := workerFor(addr)
	for _, k := range []string{"first", "bad", "later", "last"} {
		if _, err := w.enqueue(syncOp{Method: "DELETE", Path: "/db/" + k}); err != nil {
			t.Fatal(err)
		}
	}
	drained(t, w)
	if strings.Join(got, ",") != "first,later,last" {
		t.Fatalf("applied %v", got)
	}
}

// If the connection breaks before the reply, the whole batch is sent again
// in order.
func TestWorkerResendsBatchWhenReplyIsLost(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	fastRetry(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := wire.NewMux(ln, func(first byte) bool { return first == hello[0] })
	t.Cleanup(func() { _ = mux.Close() })
	var conns atomic.Int32
	mux.SetHandler(func(c net.Conn) {
		if conns.Add(1) == 1 {
			// Take the batch, apply it, and drop the connection unanswered.
			r := bufio.NewReader(c)
			w := bufio.NewWriter(c)
			buf := make([]byte, len(hello))
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			_, _ = w.WriteString(hello)
			_ = w.Flush()
			if kind, _ := r.ReadByte(); kind != reqBatch {
				return
			}
			n, _ := readCount(r, maxBatchOps)
			for range n {
				if o, err := readOp(r); err == nil {
					_ = applyOp(o)
				}
			}
			return
		}
		serveTCP(c)
	})

	w := workerFor("http://" + ln.Addr().String())
	if _, err := w.enqueue(syncOp{Method: "PUT", Path: "/db/n", Body: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.enqueue(syncOp{Method: "PUT", Path: "/db/n", Body: []byte("2")}); err != nil {
		t.Fatal(err)
	}
	drained(t, w)
	if conns.Load() != 2 || db.Get("n") != "2" {
		t.Fatalf("connections %d, n = %s", conns.Load(), db.Get("n"))
	}
}

// A peer from v0.7 or earlier answers hello with an HTTP 400 at once.
func TestDialOldPeerIsUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	start := time.Now()
	if _, err := dialPeer(srv.URL); !errors.Is(err, errPeerUnsupported) {
		t.Fatalf("dial = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v to notice", d)
	}
	if _, err := dialPeer("https://example.invalid:1"); !errors.Is(err, errPeerUnsupported) {
		t.Fatalf("https peer = %v", err)
	}
}

func TestTCPAddr(t *testing.T) {
	for in, want := range map[string]string{
		"http://10.0.0.2:23457":  "10.0.0.2:23457",
		"http://10.0.0.2:23457/": "10.0.0.2:23457",
		"10.0.0.2:23457":         "10.0.0.2:23457",
	} {
		if got, ok := tcpAddr(in); !ok || got != want {
			t.Fatalf("tcpAddr(%q) = %q, %v", in, got, ok)
		}
	}
}

func TestRestoreOverPeersProtocol(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := wire.NewMux(ln, func(first byte) bool { return first == hello[0] })
	t.Cleanup(func() { _ = mux.Close() })
	mux.SetHandler(func(c net.Conn) {
		r := bufio.NewReader(c)
		w := bufio.NewWriter(c)
		buf := make([]byte, len(hello))
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		_, _ = w.WriteString(hello)
		_ = w.Flush()
		if kind, _ := r.ReadByte(); kind == reqDocument {
			_ = wire.WriteFrame(w, []byte(`{"from":"peer","n":9007199254740993}`))
		}
		_ = w.Flush()
	})
	Restore([]string{"http://" + ln.Addr().String()})
	if got := db.Get(""); got != `{"from":"peer","n":9007199254740993}` {
		t.Fatalf("restored %s", got)
	}
}

// Records queued by v0.7, in the HTTP shape the WAL has always had, are
// sent over the peers protocol after an upgrade.
func TestQueuedRecordsFromBeforeReplay(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	addr := startPeer(t, false)
	wal := walLine(t, syncOp{Method: "PUT", Path: "/db/old", Body: []byte(`"v0.7"`), ContentType: "application/json"}) +
		"A\n" + walLine(t, syncOp{Method: "PUT", Path: "/db/kept", Body: []byte(`1`), ContentType: "application/json"})
	if err := os.MkdirAll(currentWALDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walPathForAddr(addr), []byte(wal), 0o600); err != nil {
		t.Fatal(err)
	}
	w := workerFor(addr)
	drained(t, w)
	if db.Get("kept") != "1" || db.Get("old") != "" {
		t.Fatalf("document = %s", db.Get(""))
	}
}

// Writing an address without http://, as the README now shows, must find
// the queue a v0.7 node kept under the http:// form.
func TestWALPathIgnoresScheme(t *testing.T) {
	resetSyncState(t)
	if a, b := walPathForAddr("http://10.0.0.2:23457"), walPathForAddr("10.0.0.2:23457"); a != b {
		t.Fatalf("%s != %s", a, b)
	}
}

// A peer under db.fsync always waits for an fsync per write. A batch that
// takes longer than the sender waits for its reply must still make
// progress, not be sent again whole forever.
func TestSlowPeerStillDrains(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	fastRetry(t)
	origTimeout, origBudget := batchTimeout, applyBudget
	batchTimeout, applyBudget = 200*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { batchTimeout, applyBudget = origTimeout, origBudget })
	swapApplyOp(t, func(o op) error { time.Sleep(5 * time.Millisecond); return nil })
	addr := startPeer(t, false)
	w := workerFor(addr)
	for range 100 { // 500 ms of fsyncs, over twice the wait
		if _, err := w.enqueue(syncOp{Method: "DELETE", Path: "/db/k"}); err != nil {
			t.Fatal(err)
		}
	}
	if !eventually(t, 5*time.Second, func() bool { _, ok := w.peek(); return !ok }) {
		t.Fatalf("%d ops still queued", len(pendingPaths(w)))
	}
}

// A panic while applying a batch is logged and the connection closed; the
// node stays up and the sender sends the batch again.
func TestServeTCPRecoversPanic(t *testing.T) {
	resetSyncState(t)
	useDB(t)
	fastRetry(t)
	var calls atomic.Int32
	swapApplyOp(t, func(o op) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return nil
	})
	w := workerFor(startPeer(t, false))
	if _, err := w.enqueue(syncOp{Method: "DELETE", Path: "/db/k"}); err != nil {
		t.Fatal(err)
	}
	drained(t, w)
	if calls.Load() != 2 {
		t.Fatalf("applied %d times, want the panic and one retry", calls.Load())
	}
}

package db

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// failingDisk swaps in file hooks that fail while the switches are on. It must
// run before Init, which starts the writer that calls them.
type failingDisk struct {
	writes, syncs atomic.Bool
	exits         chan string
}

func newFailingDisk(t *testing.T, tick time.Duration) *failingDisk {
	t.Helper()
	d := &failingDisk{exits: make(chan string, 4)}
	pw, ps, pf, pt := writeFile, syncFile, fatalf, tickEvery
	writeFile = func(f *os.File, b []byte) (int, error) {
		if d.writes.Load() {
			// Half the batch reaches the file, as when the disk fills up.
			n, _ := f.Write(b[:len(b)/2])
			return n, errors.New("no space left on device")
		}
		return f.Write(b)
	}
	syncFile = func(f *os.File) error {
		if d.syncs.Load() {
			return errors.New("input/output error")
		}
		return f.Sync()
	}
	fatalf = func(format string, args ...any) {
		select {
		case d.exits <- fmt.Sprintf(format, args...):
		default:
		}
	}
	tickEvery = tick
	t.Cleanup(func() {
		writeFile, syncFile, fatalf, tickEvery = pw, ps, pf, pt
		writesRefused.Store(false)
	})
	return d
}

// abandonWriter is for tests whose writer stopped as if the process had
// exited: acknowledgements it owed will never come, so reset the count.
func abandonWriter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { wg = sync.WaitGroup{} })
}

func (d *failingDisk) waitExit(t *testing.T) string {
	t.Helper()
	select {
	case msg := <-d.exits:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("the writer did not exit")
		return ""
	}
}

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// everysec: a failed write is cut back off the file, writes are refused
// while it fails, and everything accepted is written once it recovers.
func TestEverysecWriteFailureRefusesThenRecovers(t *testing.T) {
	disk := newFailingDisk(t, 5*time.Millisecond)
	dir := useAOF(t)

	if err := Set("a", 1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a is written", func() bool { return readAOF(t, dir) == "+a\n1\n" })

	disk.writes.Store(true)
	if err := Set("b", 2); err != nil {
		t.Fatalf("a write before the failure is noticed is accepted: %v", err)
	}
	eventually(t, "writes are refused", func() bool {
		return errors.Is(Set("c", 3), ErrWritesRefused)
	})
	if err := Del("a"); !errors.Is(err, ErrWritesRefused) {
		t.Fatalf("Del while refused = %v", err)
	}
	if err := Clone("a", "z"); !errors.Is(err, ErrWritesRefused) {
		t.Fatalf("Clone while refused = %v", err)
	}
	if got := readAOF(t, dir); got != "+a\n1\n" {
		t.Fatalf("partial write left in the file: %q", got)
	}
	if Get("a") != "1" || Get("b") != "2" {
		t.Fatalf("reads while refused: %s", Get(""))
	}
	if len(disk.exits) != 0 {
		t.Fatalf("everysec exited on a write failure: %s", <-disk.exits)
	}

	disk.writes.Store(false)
	eventually(t, "writes are accepted again", func() bool { return Set("d", 4) == nil })
	Close()

	resetConfig()
	Init(dir)
	defer Close()
	if got := Get(""); got != `{"a":1,"b":2,"c":3,"d":4}` && got != `{"a":1,"b":2,"d":4}` {
		t.Fatalf("reloaded %s", got)
	}
}

// A vacuum writes the whole document, so it recovers from a failed append
// and takes the pending records with it.
func TestVacuumRecoversRefusedWrites(t *testing.T) {
	disk := newFailingDisk(t, time.Hour) // no retry: only the vacuum can recover
	dir := useAOF(t)

	disk.writes.Store(true)
	if err := Set("b", 2); err != nil {
		t.Fatal(err)
	}
	Vacuum()
	eventually(t, "writes are refused", func() bool {
		return errors.Is(Set("c", 3), ErrWritesRefused)
	})

	disk.writes.Store(false)
	Vacuum()
	eventually(t, "the vacuum lets writes in", func() bool { return Set("d", 4) == nil })
	Close()
	if got := readAOF(t, dir); got[:2] != "*\n" {
		t.Fatalf("AOF does not start with the vacuum snapshot: %q", got)
	}
	resetConfig()
	Init(dir)
	defer Close()
	if got := Get("b"); got != "2" {
		t.Fatalf("pending write lost across the vacuum: %s", Get(""))
	}
}

// always: a writer waiting for its acknowledgement must not get one, so the
// process exits instead and the restart reloads the file.
func TestAlwaysWriteFailureExits(t *testing.T) {
	disk := newFailingDisk(t, time.Hour)
	withFsyncPolicy(t, FsyncAlways)
	abandonWriter(t)
	dir := useAOF(t)
	if err := Set("a", 1); err != nil {
		t.Fatal(err)
	}

	disk.writes.Store(true)
	acked := make(chan error, 1)
	go func() { acked <- Set("b", 2) }()
	if msg := disk.waitExit(t); msg == "" {
		t.Fatal("empty exit message")
	}
	select {
	case err := <-acked:
		t.Fatalf("the writer was answered (%v) instead of the process exiting", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := readAOF(t, dir); got != "+a\n1\n" {
		t.Fatalf("partial write left in the file: %q", got)
	}
}

func TestFsyncFailureExits(t *testing.T) {
	for _, policy := range []FsyncPolicy{FsyncEverysec, FsyncAlways} {
		disk := newFailingDisk(t, 5*time.Millisecond)
		withFsyncPolicy(t, policy)
		abandonWriter(t)
		useAOF(t)
		disk.syncs.Store(true)
		go Set("k", 1) // under always it waits forever
		if msg := disk.waitExit(t); msg == "" {
			t.Fatalf("policy %d: empty exit message", policy)
		}
		Close()
	}
}

package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const host = "127.0.0.1:23466"

var pool = sync.Pool{
	New: func() any {
		nc, _ := net.Dial("tcp4", host)
		return nc
	},
}

var (
	exit         atomic.Bool
	total        uint64
	connected    uint64
	failed       uint64
	maxlatency   time.Duration
	minlatency   time.Duration = time.Second
	totallatency time.Duration
	statsMu      sync.Mutex
	jobs         = make(chan struct{})
	wgr          sync.WaitGroup
)

func conn() {
	defer wgr.Done()
	nc, err := net.Dial("tcp4", host)
	if err != nil {
		atomic.AddUint64(&failed, 1)
		return
	}
	defer nc.Close()
	atomic.AddUint64(&connected, 1)
	r := bufio.NewReader(nc)
	w := bufio.NewWriter(nc)

	for range jobs {

		ts := time.Now()

		//set(r, w)
		//get(r, w)
		//clone(r, w)
		del(r, w)

		atomic.AddUint64(&total, 1)

		n := time.Since(ts)
		statsMu.Lock()
		if n > maxlatency {
			maxlatency = n
		} else if n < minlatency {
			minlatency = n
		}
		totallatency += n
		statsMu.Unlock()
	}
}

func set(r *bufio.Reader, w *bufio.Writer) error {
	if _, err := w.WriteString("+bench\n\"mark\"\n"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := r.ReadBytes('\n'); err != nil {
		return err
	}
	return nil
}

func get(r *bufio.Reader, w *bufio.Writer) error {
	if _, err := w.WriteString("=\n"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := r.ReadBytes('\n'); err != nil {
		return err
	}
	if _, err := r.ReadBytes('\n'); err != nil {
		return err
	}
	return nil
}

func clone(r *bufio.Reader, w *bufio.Writer) error {
	if _, err := w.WriteString("<bench\n>mark\n"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := r.ReadBytes('\n'); err != nil {
		return err
	}
	return nil
}

func del(r *bufio.Reader, w *bufio.Writer) error {
	if _, err := w.WriteString("-bench\n"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := r.ReadBytes('\n'); err != nil {
		return err
	}
	return nil
}

func main() {
	duration := flag.Duration("d", 10*time.Second, "Duration of test")
	numberOfConnections := flag.Int("c", 120, "Connections to keep open")
	flag.Parse()

	for i := 0; i < *numberOfConnections; i++ {
		wgr.Add(1)
		go conn()
	}
	wgr.Wait()
	connectedNow := atomic.LoadUint64(&connected)
	if connectedNow == 0 {
		fmt.Printf("Cannot connect to %s (failed: %d). Start server first.\n", host, atomic.LoadUint64(&failed))
		return
	}

	fmt.Printf("Running %v test @ %s\n", duration, host)
	fmt.Printf("  %d connections\n", connectedNow)

	startAt := time.Now()
	go func() {
		for !exit.Load() {
			jobs <- struct{}{}
		}
		close(jobs)
	}()

	<-time.After(*duration)
	exit.Store(true)
	spent := time.Since(startAt)
	totalReq := atomic.LoadUint64(&total)

	statsMu.Lock()
	avgLatency := time.Duration(0)
	if totalReq > 0 {
		avgLatency = totallatency / time.Duration(totalReq)
	}
	localMax := maxlatency
	localMin := minlatency
	statsMu.Unlock()

	fmt.Println("  Stats\t\tAvg\t\tMin\t\tMax")
	fmt.Printf("  Latency\t%s\t%s\t%s\n", avgLatency, localMin, localMax)
	fmt.Printf("  %d requests in %s\n", totalReq, spent)
	fmt.Printf("Requests/sec: %.2f\n", float64(totalReq)/float64(spent.Seconds()))
}

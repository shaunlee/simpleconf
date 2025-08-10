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
	exit bool = false
	total uint64
	maxlatency time.Duration
	minlatency time.Duration = time.Second
	totallatency time.Duration
	jobs = make(chan struct{})
	wgr sync.WaitGroup
)

func conn() {
	nc, _ := net.Dial("tcp4", "127.0.0.1:23466")
	defer nc.Close()
	r := bufio.NewReader(nc)
	w := bufio.NewWriter(nc)
	wgr.Done()

	for !exit {
		<-jobs

		ts := time.Now()

		//set(r, w)
		//get(r, w)
		//clone(r, w)
		del(r, w)

		atomic.AddUint64(&total, 1)

		n := time.Since(ts)
		if n > maxlatency {
			maxlatency = n
		} else if n < minlatency {
			minlatency = n
		}
		totallatency += n
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
	duration := flag.Duration("d", 10 * time.Second, "Duration of test")
	numberOfConnections := flag.Int("c", 120, "Connections to keep open")
	flag.Parse()

	for i := 0; i < *numberOfConnections; i++ {
		wgr.Add(1)
		go conn()
	}
	wgr.Wait()

	fmt.Printf("Running %v test @ %s\n", duration, host)
	fmt.Printf("  %d connections\n", *numberOfConnections)

	startAt := time.Now()
	go func() {
		for !exit {
			jobs <- struct{}{}
		}
	}()

	<-time.After(*duration)
	exit = true
	spent := time.Since(startAt)

	fmt.Println("  Stats\t\tAvg\t\tMin\t\tMax")
	fmt.Printf("  Req/Sec\t%s\t%s\t%s\n", totallatency / time.Duration(total), maxlatency, minlatency)
	fmt.Printf("  %d requests in %s\n", total, spent)
	fmt.Printf("Requests/sec: %.2f\n", float64(total) / float64(spent.Seconds()))
}

# Benchmarks

Measured on an Apple M6 (12 logical cores, 32 GB), macOS 27.

Everything that crosses a socket — both charts and the throughput tables
below — was measured inside a Linux VM on this machine (OrbStack, Debian 12
containers on host networking, data on VM-native volumes), with the client on
the same machine. etcd, Consul and Valkey only run there, and macOS's loopback
stack is several times slower than Linux's, so putting every server *and* every
client in the VM is what keeps the comparison like for like. The in-memory
benchmarks, which touch no socket, are native macOS.

## Compared with similar tools

Single-key throughput against etcd, Consul and Valkey, all run from their
official Docker images on the same machine with host networking.

![HTTP API throughput: simpleconf, Consul, etcd](images/bench-http.svg)

![Native protocol throughput: simpleconf TCP, Valkey RESP](images/bench-native.svg)

Against Valkey the win is narrow and one-sided: simpleconf leads decisively
only on pipelined reads, at 16.5M GET/s against 6.66M. Unpipelined, and on
pipelined writes, Valkey is ahead.

The comparison is not like for like, and the HTTP gaps in particular overstate
the difference in implementation:

- simpleconf and Valkey append to a file fsynced once a second; etcd and
  Consul replicate every write through Raft and fsync it before replying.
- etcd is measured through its HTTP/JSON gateway, not its native gRPC API; a
  local `serializable` read is no faster (42.2k/s), so the gateway is the limit.
- Consul runs as a single server with data on disk, not in `-dev` mode.
- Valkey is shown with the better of two settings at each depth: `--io-threads
  4` at depth 1 (545k GET / 480k SET with the default single thread), the
  default at depth 64 (4.44M GET / 2.45M SET with `--io-threads 4`). It
  executes commands on one thread, while simpleconf serves reads in parallel.
- Valkey is driven by `valkey-benchmark --threads 8`, simpleconf by its own Go
  client, so the client overhead differs.

## TCP pipelining

Because the server only flushes before a read that would block, a client that
pipelines commands collects many replies per write syscall. Measured with 50
connections and `db.fsync: everysec`, against a 16-byte document and a 21.8 KB
one — the figures track each other, which is the point of the representation:

```text
          16 B document            21.8 KB document
depth     GET        SET          GET        SET
1          808k/s     681k/s       806k/s     682k/s
8         5.65M/s    2.09M/s      5.67M/s    2.06M/s
64       16.47M/s    2.76M/s     17.40M/s    3.06M/s
```

## HTTP

`wrk -t4`, same configuration and the same two documents:

```text
              16 B document                  21.8 KB document
conns    GET       SET       DEL        GET       SET       DEL
10       456k/s    493k/s    506k/s     442k/s    523k/s    530k/s
200      1.02M/s   674k/s    707k/s     1.02M/s   708k/s    732k/s
```

With 200 connections HTTP GET edges past unpipelined TCP, but it stays far
below a pipelined one, and for two reasons: HTTP has no pipelining, and a reply
averages around 105 bytes of status line and headers against 10 bytes for the
TCP `GET` reply `$6\n"mark"\n`.

## In-memory operations

Pure function calls against the in-memory document — no syscalls, no network.

```text
cpu: Apple M6
BenchmarkGet-12      	228148927	        10.47 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-12      	 82901794	        29.89 ns/op	      24 B/op	       2 allocs/op
BenchmarkDel-12      	270620913	         8.944 ns/op	       0 B/op	       0 allocs/op
BenchmarkClone-12    	 84426901	        28.84 ns/op	      16 B/op	       1 allocs/op
```

## Scaling with document size

The document is held as a tree of ordered objects, arrays and raw scalar
leaves, so a keyed read or write costs O(depth). It does not depend on how
large the document is, nor on where in it the key sits. Whole-document reads
are served from a snapshot that is rebuilt only after a write.

```text
BenchmarkDocSize/60B/Get_first-12   	209001240	        11.50 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60B/Get_last-12    	204285433	        11.74 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60B/Get_whole-12   	789801250	         3.048 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60B/Set_last-12    	 80331366	        29.97 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/600B/Get_last-12   	185648116	        12.59 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/600B/Set_last-12   	 77537744	        30.38 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/6KB/Get_last-12    	206818244	        11.44 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/6KB/Set_last-12    	 81085989	        29.31 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/60KB/Get_first-12  	198406594	        12.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60KB/Get_last-12   	201218524	        11.75 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60KB/Get_whole-12  	784765947	         3.069 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60KB/Set_last-12   	 80557075	        29.65 ns/op	      24 B/op	       2 allocs/op
```

Two costs still grow with the document: rebuilding the snapshot after a write,
paid on the next whole-document read, and holding the tree, which takes about
ten times the document's own size against roughly two for the single string it
replaced. `go test ./internal/db/ -run TestFootprint -v` reports both:

```text
600B   doc=   576B | tree=  4207B (7.3x doc)  | string+snapshot=  1219B (2.1x doc) | tree/string=3.5x
6KB    doc=  6400B | tree= 65156B (10.2x doc) | string+snapshot= 13129B (2.1x doc) | tree/string=5.0x
60KB   doc= 68280B | tree=659775B (9.7x doc)  | string+snapshot=147534B (2.2x doc) | tree/string=4.5x
```

Earlier releases held the document as one JSON string. Reads of an early key
were constant-time, but everything else was proportional to the key's offset:
at 60 KB, reading the last key cost 31 µs and rewriting it 14 µs (measured on
the previous machine, against an implementation that no longer exists). It also
meant inserting an unrelated key near the front of the document slowed down
every key behind it. Both properties are gone.

## TCP protocol

`go test ./internal/tcpapi/ -bench Tcp`. The serial benchmarks use a single connection
with a strict request → response ping-pong, so they measure **round-trip
latency**, not throughput. The `*Parallel` variants use one connection per
goroutine.

```text
cpu: Apple M6 (Linux VM)
BenchmarkTcpGet-12              	  949948	      2540 ns/op	      56 B/op	       6 allocs/op
BenchmarkTcpSet-12              	  931627	      2588 ns/op	      80 B/op	       8 allocs/op
BenchmarkTcpDel-12              	  958932	      2520 ns/op	      32 B/op	       4 allocs/op
BenchmarkTcpClone-12            	  928945	      2587 ns/op	      80 B/op	       8 allocs/op

BenchmarkTcpGetParallel-12      	 1779358	      1322 ns/op	      59 B/op	       6 allocs/op
BenchmarkTcpSetParallel-12      	 1738848	      1353 ns/op	      83 B/op	       8 allocs/op
BenchmarkTcpDelParallel-12      	 1780437	      1341 ns/op	      32 B/op	       4 allocs/op
BenchmarkTcpCloneParallel-12    	 1747497	      1375 ns/op	      81 B/op	       8 allocs/op
```

## Raft

`go test ./internal/cluster/ -bench RaftApply -benchtime 5000x`, a single-node
cluster, in a Linux container on the same machine. Every write is a Raft log
entry. Under `raft.fsync: always` it is fsynced before it is applied, and Raft
stores entries in batches, so concurrent writers share an fsync. Under
`everysec` the fsync happens in the background once a second:

```text
          always                   everysec
writers   per write   writes/s     per write   writes/s
1         590 µs      ~1.7k        40 µs       ~25k
12        146 µs      ~6.8k        39 µs       ~26k
64         46 µs      ~22k         23 µs       ~43k
```

With `everysec` one writer and twelve take about the same time: the disk is no
longer the limit, Raft's own pipeline is. On macOS, where Go's `fsync` is
`F_FULLFSYNC`, one writer under `always` takes about 4 ms per write.

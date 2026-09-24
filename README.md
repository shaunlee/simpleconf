# simpleconf

[![test](https://github.com/shaunlee/simpleconf/actions/workflows/test.yml/badge.svg)](https://github.com/shaunlee/simpleconf/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/shaunlee/simpleconf/graph/badge.svg)](https://codecov.io/gh/shaunlee/simpleconf)
[![Go Report Card](https://goreportcard.com/badge/github.com/shaunlee/simpleconf)](https://goreportcard.com/report/github.com/shaunlee/simpleconf)

`simpleconf` is a lightweight configuration service that can be operated over HTTP/TCP, with support for:

- JSON key-path read/write (for example `product.name`)
- Local append-only persistence
- Optional Raft cluster mode (no single point of failure)

## Benchmarks

Measured on an Apple M6 (12 logical cores, 32 GB), macOS 27.

Everything that crosses a socket — both charts and the throughput tables
below — was measured inside a Linux VM on this machine (OrbStack, Debian 12
containers on host networking, data on VM-native volumes), with the client on
the same machine. etcd, Consul and Valkey only run there, and macOS's loopback
stack is several times slower than Linux's, so putting every server *and* every
client in the VM is what keeps the comparison like for like. The in-memory
benchmarks, which touch no socket, are native macOS.

### Compared with similar tools

Single-key throughput against etcd, Consul and Valkey, all run from their
official Docker images on the same machine with host networking.

![HTTP API throughput: simpleconf, Consul, etcd](docs/images/bench-http.svg)

![Native protocol throughput: simpleconf TCP, Valkey RESP](docs/images/bench-native.svg)

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

### TCP pipelining

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

### HTTP

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

### In-memory operations

Pure function calls against the in-memory document — no syscalls, no network.

```text
cpu: Apple M6
BenchmarkGet-12      	228148927	        10.47 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-12      	 82901794	        29.89 ns/op	      24 B/op	       2 allocs/op
BenchmarkDel-12      	270620913	         8.944 ns/op	       0 B/op	       0 allocs/op
BenchmarkClone-12    	 84426901	        28.84 ns/op	      16 B/op	       1 allocs/op
```

### Scaling with document size

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

### TCP protocol

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

## Quick Start

Requirements:

- Go 1.25+

Run:

```bash
go run ./cmd/simpleconf
```

By default, HTTP listens on `:23456`.

## Configuration File

The service reads `configs/config.yml`.

Minimal config:

```yaml
db:
  dir: data
listen: :23456
raft:
  enabled: false
```

Common config keys:

- `db.dir`: data directory
- `db.fsync`: when to fsync the append-only file — `always`, `everysec` (default) or `no`
- `listen`: HTTP listen address
- `tcp.listen`: TCP listen address (TCP is disabled if not set)
- `raft.enabled`: enable/disable Raft
- `raft.forward`: whether followers auto-forward writes to leader (default `true`)
- `raft.node_id`: Raft node ID
- `raft.listen`: Raft transport address
- `raft.http_addr`: HTTP address reachable by other nodes/clients
- `raft.bootstrap`: set `true` only on the first node during initial bootstrap
- `raft.peers`: format `id,raft_addr,http_addr`

### Durability

The append-only file is written by a background writer that batches records, so
a burst of writes costs one write syscall rather than one per record. When the
file is fsynced is controlled by `db.fsync`:

| `db.fsync` | Survives `kill -9` | Survives power loss | Cost |
| --- | --- | --- | --- |
| `always` | yes | yes | writes block until fsync completes |
| `everysec` (default) | yes | up to 1s of writes lost | negligible |
| `no` | yes | lost until the OS flushes | none |

Under `always` a successful `PUT`/`DELETE`/clone means the record is on disk;
concurrent writers share one fsync through group commit. The cost is real, and
it is dominated by the storage underneath rather than by simpleconf. The same
`wrk -t4 -c200` SET test, `always` against `everysec`:

| Storage | `always` | `everysec` |
| --- | --- | --- |
| Linux VM volume (the benchmarks above) | 189k/s | 661k/s |
| macOS APFS/NVMe, native | 24.6k/s | 186k/s |
| ext4/NVMe, previous machine | 19.5k/s | 237k/s |

A virtual disk makes fsync far cheaper than a physical one, so take the 3.5x
ratio in the first row as the floor and the ~10x in the other two as what to
expect on real hardware.

`everysec` and `no` both survive `kill -9`, because the writer flushes each
batch to the kernel before going idle; they differ only in exposure to power
loss.

## Key Paths

A key path selects a value inside the document. Segments are separated by `.`;
a `\` escapes the next character, so a key containing a literal dot is written
`fav\.movie`.

```bash
curl -s localhost:23456/db/name.first
curl -s localhost:23456/db/friends.0.last
curl -s 'localhost:23456/db/fav\.movie'
```

Reads also accept [gjson](https://github.com/tidwall/gjson) query syntax:

| Path | Meaning |
| --- | --- |
| `friends.#` | number of elements |
| `friends.#.first` | that field from every element |
| `friends.#(age>45)` | the first element matching |
| `friends.#(age>45)#` | every element matching |
| `friends.#(age>45)#.first` | that field from every match |
| `friends.#(first%"D*")#` | `%` matches a pattern, `!%` negates it |
| `fri*`, `n?me` | `*` matches any run of characters, `?` exactly one |

Comparisons accept `==`, `=`, `!=`, `<`, `<=`, `>` and `>=`. A bare path such
as `#(first)` tests that the field exists, and `#(!first)` that it does not.
gjson's `@modifiers` and `|` pipes are not supported.

```text
=friends.#                  $1
                            3
=friends.#(age>45)#.first   $16
                            ["Roger","Jane"]
```

Note that `#` and `?` cannot be used over HTTP. The key is taken from the URL
path without percent-decoding, so `%23` stays `%23` rather than becoming `#`,
and an unencoded `#` or `?` is consumed by the client as a fragment or query
string. Queries using them are reachable over the TCP protocol only; `*`
works over both.

Writes take plain paths, with two extras from
[sjson](https://github.com/tidwall/sjson): an index past the end of an array
pads it with `null`, and index `-1` appends.

```bash
curl -s -X PUT localhost:23456/db/ports -d '[80,443]'
curl -s -X PUT localhost:23456/db/ports.5 -d '8080'
curl -s localhost:23456/db/ports      # [80,443,null,null,null,8080]
curl -s -X PUT localhost:23456/db/ports.-1 -d '9090'
curl -s localhost:23456/db/ports      # [80,443,null,null,null,8080,9090]
curl -s -X DELETE localhost:23456/db/ports.0
curl -s localhost:23456/db/ports      # [443,null,null,null,8080,9090]
```

## HTTP Usage

Endpoints:

- `GET /`: basic metadata
- `GET /db`: get full JSON document
- `GET /db/:key`: get key-path value
- `PUT /db/:key`: set key-path value (raw JSON body)
- `DELETE /db/:key`: delete key path
- `POST /clone/:from_key/:to_key`: clone value
- `POST /vacuum`: rewrite append-only file

Examples:

```bash
curl -s -X PUT http://localhost:23456/db/product.name -d '"Demo"'
curl -s -X PUT http://localhost:23456/db/product.year -d '2026'
curl -s http://localhost:23456/db/product
curl -s -X DELETE http://localhost:23456/db/product.year
curl -s -X POST http://localhost:23456/clone/product.name/product.alias
```

Original example (httpie):

```bash
echo '2017' | http http://localhost:23456/db/product.year
echo '"Demo"' | http http://localhost:23456/db/product.name
echo 'false' | http http://localhost:23456/db/product.is_expired

http http://localhost:23456/db/product
{
    "is_expired": false,
    "name": "Demo",
    "year": 2017
}

http delete http://localhost:23456/db/product.is_expired

http http://localhost:23456/db/product
{
    "name": "Demo",
    "year": 2017
}
```

## TCP Protocol

Commands:

- `=`: get full JSON document
- `=key.path`: get key-path value
- `+key.path` + next line raw JSON: set value
- `-key.path`: delete value
- `<from.key.path` + next line `>to.key.path`: clone
- `*`: vacuum
- `PING`: returns `+PONG`

Example:

```text
+product.name
"Demo"
=product.name
```

Protocol Reference:

- Get whole configuration
- HTTP: `GET /db`
- TCP: `=`
- Get value by key path
- HTTP: `GET /db/{key.path}`
- TCP: `=key.path`
- Set value by key path
- HTTP: `PUT /db/{key.path} {"name":"Demo"}`
- TCP:
```text
+key.path
{"name":"Demo"}
```
- Delete key path
- HTTP: `DELETE /db/{key.path}`
- TCP: `-key.path`
- Clone key path
- HTTP: `POST /clone/{from.key.path}/{to.key.path}`
- TCP:
```text
<from.key.path
>to.key.path
```
- Vacuum
- HTTP: `POST /vacuum`
- TCP: `*`
- Ping
- TCP: `PING`

## Raft Cluster Usage

Enable Raft:

1. Set `raft.enabled: true` on all nodes
2. Use different `raft.node_id` / `raft.listen` / `listen` / `db.dir` per node
3. Use the same `raft.peers` list on all nodes
4. Set `raft.bootstrap: true` only on the first node during first cluster bring-up

Write behavior:

- `raft.forward=true`: writes sent to followers are auto-forwarded to leader
- `raft.forward=false`: followers return leader info
- HTTP: `409` + `{"error":"not leader","leader":"http://..."}`
- TCP: `-ERR not leader http://...`

Persistence:

- Application data: `db.dir/data.aof`
- Raft state: `db.dir/raft`
- No BoltDB dependency

## Legacy Peers Mode

When `raft.enabled=false`, legacy peers sync mode is available (compatibility mode):

- `peers.addresses`
- `peers.listen`

For production clusters, Raft mode is recommended.

## License

MIT

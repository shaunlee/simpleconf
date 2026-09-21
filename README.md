# simpleconf

`simpleconf` is a lightweight configuration service that can be operated over HTTP/TCP, with support for:

- JSON key-path read/write (for example `product.name`)
- Local append-only persistence
- Optional Raft cluster mode (no single point of failure)

## Benchmarks

Measured on an AMD Ryzen 9 5900HX (16 logical cores, Linux), with the client on
the same machine, so the throughput figures are conservative.

### In-memory operations

Pure function calls against the in-memory document — no syscalls, no network.

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkGet-16      	48848946	        48.02 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-16      	12734566	       189.0 ns/op	      96 B/op	       3 allocs/op
BenchmarkDel-16      	16635156	       135.3 ns/op	      80 B/op	       3 allocs/op
BenchmarkClone-16    	 9288730	       261.9 ns/op	     168 B/op	       4 allocs/op
```

### Scaling with document size

The document is held as a single JSON string, read with `gjson` and rewritten
with `sjson`. That makes reads of an early key constant-time regardless of
document size, but two costs grow:

- `Get` scans from the start, so a key late in the document costs proportionally
  to its offset.
- `Set` rebuilds the whole string, so it costs — and allocates — proportionally
  to the document size.

```text
BenchmarkDocSize/60B/Get_first-16      	        94.20 ns/op
BenchmarkDocSize/60B/Get_last-16       	        93.20 ns/op
BenchmarkDocSize/60B/Set-16            	       433.5 ns/op	     256 B/op
BenchmarkDocSize/591B/Get_last-16      	       370.9 ns/op
BenchmarkDocSize/591B/Set-16           	       674.7 ns/op	    1384 B/op
BenchmarkDocSize/5991B/Get_last-16     	      3168 ns/op
BenchmarkDocSize/5991B/Set-16          	      1774 ns/op	   12520 B/op
BenchmarkDocSize/60891B/Get_first-16   	        94.45 ns/op
BenchmarkDocSize/60891B/Get_last-16    	     31213 ns/op
BenchmarkDocSize/60891B/Set-16         	     13767 ns/op	  131305 B/op
```

This suits a configuration service, where documents are small and writes are
rare, and it is what buys the zero-allocation `Get`. It is a poor fit for a
large, write-heavy document: at 60 KB a write costs 13.8 µs and discards 131 KB.
The representation is also what provides the `gjson`/`sjson` key-path semantics
the API exposes — array indexing (`svc.ports.1`), appending and padding, index
deletion with shifting, and escaped dots (`m.x\.y`).

### TCP protocol

`go test ./server/ -bench Tcp`. The serial benchmarks use a single connection
with a strict request → response ping-pong, so they measure **round-trip
latency**, not throughput. The `*Parallel` variants use one connection per
goroutine.

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpGet-16              	  121798	     19978 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpSet-16              	  116130	     20378 ns/op	     176 B/op	      11 allocs/op
BenchmarkTcpDel-16              	  111019	     20064 ns/op	     104 B/op	       7 allocs/op
BenchmarkTcpClone-16            	  114981	     20104 ns/op	     232 B/op	      11 allocs/op

BenchmarkTcpGetParallel-16      	  912423	      2707 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpSetParallel-16      	  882728	      2875 ns/op	     227 B/op	      12 allocs/op
BenchmarkTcpDelParallel-16      	  764455	      3268 ns/op	     128 B/op	       7 allocs/op
BenchmarkTcpCloneParallel-16    	  696648	      3106 ns/op	     217 B/op	      12 allocs/op
```

A `-cpu` sweep of the parallel GET shows `-cpu 1` reproducing the serial number
exactly, near-linear scaling to the core count, and flat allocations per
operation — the extra time in the serial case is syscalls and scheduling, not
the database.

```text
BenchmarkTcpGetParallel         	     19324 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-2       	     10993 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-4       	      6485 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-8       	      3767 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-16      	      2632 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-64      	      2272 ns/op	      65 B/op	       8 allocs/op
BenchmarkTcpGetParallel-256     	      3364 ns/op	      70 B/op	       8 allocs/op
```

Because the server only flushes before a read that would block, a client that
pipelines commands collects many replies per write syscall. Measured with 50
connections, `db.fsync: everysec`, data directory on tmpfs:

```text
depth   GET             SET
1         339k req/s      286k req/s
8        2.12M req/s      693k req/s
64       8.33M req/s     1.14M req/s
```

### HTTP

`wrk -t4`, same configuration:

```text
connections   GET             SET             DEL
10             173k req/s      134k req/s      143k req/s
200            256k req/s      237k req/s      241k req/s
```

HTTP tops out below the raw TCP protocol mainly because of response size: a
reply averages around 105 bytes of status line and headers, against 10 bytes
for the TCP `GET` reply `$6\n"mark"\n`.

## Quick Start

Requirements:

- Go 1.25+

Run:

```bash
go run ./cmd/bin/main.go
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
concurrent writers share one fsync through group commit. The cost is real: on
ext4/NVMe, SET throughput measured 19.5k/s under `always` against 237k/s under
`everysec`.

`everysec` and `no` both survive `kill -9`, because the writer flushes each
batch to the kernel before going idle; they differ only in exposure to power
loss.

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

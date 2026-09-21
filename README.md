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
BenchmarkGet-16      	22905945	       101.5 ns/op	      80 B/op	       3 allocs/op
BenchmarkSet-16      	 9461450	       241.6 ns/op	     136 B/op	       8 allocs/op
BenchmarkDel-16      	29906817	        73.41 ns/op	      69 B/op	       2 allocs/op
BenchmarkClone-16    	 7213230	       347.4 ns/op	     216 B/op	      11 allocs/op
```

### Scaling with document size

The document is held as a tree of ordered objects, arrays and raw scalar
leaves, so a keyed read or write costs O(depth). It does not depend on how
large the document is, nor on where in it the key sits. Whole-document reads
are served from a snapshot that is rebuilt only after a write.

```text
BenchmarkDocSize/60B/Get_first-16      	        98.73 ns/op	      80 B/op
BenchmarkDocSize/60B/Get_last-16       	        95.41 ns/op	      80 B/op
BenchmarkDocSize/60B/Get_whole-16      	         5.60 ns/op	       0 B/op
BenchmarkDocSize/60B/Set_last-16       	       231.8 ns/op	     136 B/op
BenchmarkDocSize/600B/Get_last-16      	        99.37 ns/op	      80 B/op
BenchmarkDocSize/600B/Set_last-16      	       241.8 ns/op	     136 B/op
BenchmarkDocSize/6KB/Get_last-16       	        98.52 ns/op	      80 B/op
BenchmarkDocSize/6KB/Set_last-16       	       238.9 ns/op	     136 B/op
BenchmarkDocSize/60KB/Get_first-16     	       109.6 ns/op	      80 B/op
BenchmarkDocSize/60KB/Get_last-16      	       103.3 ns/op	      80 B/op
BenchmarkDocSize/60KB/Get_whole-16     	         5.61 ns/op	       0 B/op
BenchmarkDocSize/60KB/Set_last-16      	       242.1 ns/op	     136 B/op
```

Two costs still grow with the document: rebuilding the snapshot after a write,
paid on the next whole-document read, and holding the tree, which takes roughly
five times the document's own size — about 776 KB for a 60 KB document.

Earlier releases held the document as one JSON string. Reads of an early key
were constant-time, but everything else was proportional to the key's offset:
at 60 KB, reading the last key cost 31 µs and rewriting it 14 µs. It also meant
inserting an unrelated key near the front of the document slowed down every key
behind it. Both properties are gone.

### TCP protocol

`go test ./server/ -bench Tcp`. The serial benchmarks use a single connection
with a strict request → response ping-pong, so they measure **round-trip
latency**, not throughput. The `*Parallel` variants use one connection per
goroutine.

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpGet-16              	  118412	     19916 ns/op	     144 B/op	      11 allocs/op
BenchmarkTcpSet-16              	  115828	     20311 ns/op	     224 B/op	      16 allocs/op
BenchmarkTcpDel-16              	  124525	     19421 ns/op	      96 B/op	       6 allocs/op
BenchmarkTcpClone-16            	  120098	     20226 ns/op	     280 B/op	      18 allocs/op

BenchmarkTcpGetParallel-16      	  907545	      2671 ns/op	     142 B/op	      11 allocs/op
BenchmarkTcpSetParallel-16      	  910816	      2624 ns/op	     221 B/op	      16 allocs/op
BenchmarkTcpDelParallel-16      	  830340	      2833 ns/op	     101 B/op	       6 allocs/op
BenchmarkTcpCloneParallel-16    	  774948	      2898 ns/op	     274 B/op	      18 allocs/op
```

Because the server only flushes before a read that would block, a client that
pipelines commands collects many replies per write syscall. Measured with 50
connections and `db.fsync: everysec`, against a 16-byte document and a 21.8 KB
one — the figures track each other, which is the point of the representation:

```text
          16 B document          21.8 KB document
depth     GET        SET         GET        SET
1          322k/s     276k/s      307k/s     286k/s
8         1.95M/s     783k/s     2.14M/s     818k/s
64        5.97M/s    1.22M/s     5.96M/s    1.24M/s
```

### HTTP

`wrk -t4`, same configuration and the same two documents:

```text
              16 B document              21.8 KB document
conns    GET      SET      DEL      GET      SET      DEL
10        144k     115k     120k     146k     116k     125k
200       246k     230k     237k     246k     222k     234k
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

# simpleconf

`simpleconf` is a lightweight configuration service that can be operated over HTTP/TCP, with support for:

- JSON key-path read/write (for example `product.name`)
- Local append-only persistence
- Optional Raft cluster mode (no single point of failure)

## Benchmarks

Measured on an AMD Ryzen 9 5900HX (16 logical cores, Linux), with the client on
the same machine, so the throughput figures are conservative.

### TCP pipelining

Because the server only flushes before a read that would block, a client that
pipelines commands collects many replies per write syscall. Measured with 50
connections and `db.fsync: everysec`, against a 16-byte document and a 21.8 KB
one — the figures track each other, which is the point of the representation:

```text
          16 B document          21.8 KB document
depth     GET        SET         GET        SET
1          310k/s     253k/s      283k/s     239k/s
8         2.03M/s     845k/s     2.05M/s     885k/s
64        8.46M/s    1.28M/s     8.65M/s    1.44M/s
```

### HTTP

`wrk -t4`, same configuration and the same two documents:

```text
              16 B document                  21.8 KB document
conns    GET       SET       DEL        GET       SET       DEL
10       137k/s    110k/s    124k/s     153k/s    118k/s    124k/s
200      237k/s    225k/s    233k/s     230k/s    221k/s    237k/s
```

HTTP tops out below the raw TCP protocol mainly because of response size: a
reply averages around 105 bytes of status line and headers, against 10 bytes
for the TCP `GET` reply `$6\n"mark"\n`.

### In-memory operations

Pure function calls against the in-memory document — no syscalls, no network.

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkGet-16      	18202971	        69.44 ns/op	      24 B/op	       2 allocs/op
BenchmarkSet-16      	 5659772	       209.1 ns/op	      80 B/op	       7 allocs/op
BenchmarkDel-16      	24196832	        49.55 ns/op	      16 B/op	       1 allocs/op
BenchmarkClone-16    	 4524405	       269.1 ns/op	     104 B/op	       9 allocs/op
```

### Scaling with document size

The document is held as a tree of ordered objects, arrays and raw scalar
leaves, so a keyed read or write costs O(depth). It does not depend on how
large the document is, nor on where in it the key sits. Whole-document reads
are served from a snapshot that is rebuilt only after a write.

```text
BenchmarkDocSize/60B/Get_first-16   	 17567499	       72.06 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/60B/Get_last-16    	 17037295	       69.59 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/60B/Get_whole-16   	205617865	       5.824 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60B/Set_last-16    	  5832117	       206.6 ns/op	      80 B/op	       7 allocs/op
BenchmarkDocSize/600B/Get_last-16   	 15943088	       72.83 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/600B/Set_last-16   	  5669923	       207.9 ns/op	      80 B/op	       7 allocs/op
BenchmarkDocSize/6KB/Get_last-16    	 16001305	       76.12 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/6KB/Set_last-16    	  5620524	       203.7 ns/op	      80 B/op	       7 allocs/op
BenchmarkDocSize/60KB/Get_first-16  	 15006763	       78.54 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/60KB/Get_last-16   	 14920532	       77.65 ns/op	      24 B/op	       2 allocs/op
BenchmarkDocSize/60KB/Get_whole-16  	212638714	       5.708 ns/op	       0 B/op	       0 allocs/op
BenchmarkDocSize/60KB/Set_last-16   	  5909949	       216.6 ns/op	      80 B/op	       7 allocs/op
```

Two costs still grow with the document: rebuilding the snapshot after a write,
paid on the next whole-document read, and holding the tree, which takes about
ten times the document's own size against roughly two for the single string it
replaced. `go test ./db/ -run TestFootprint -v` reports both:

```text
600B   doc=   576B | tree=  4864B (8.4x doc)  | string+snapshot=  1236B (2.1x doc)
6KB    doc=  6400B | tree= 70954B (11.1x doc) | string+snapshot= 13146B (2.1x doc)
60KB   doc= 68280B | tree=720164B (10.5x doc) | string+snapshot=147548B (2.2x doc)
```

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
BenchmarkTcpGet-16              	   62134	     20174 ns/op	      80 B/op	       8 allocs/op
BenchmarkTcpSet-16              	   58096	     20083 ns/op	     168 B/op	      15 allocs/op
BenchmarkTcpDel-16              	   63088	     19194 ns/op	      48 B/op	       5 allocs/op
BenchmarkTcpClone-16            	   61641	     19933 ns/op	     168 B/op	      16 allocs/op

BenchmarkTcpGetParallel-16      	  459609	      2697 ns/op	      84 B/op	       8 allocs/op
BenchmarkTcpSetParallel-16      	  457113	      2754 ns/op	     166 B/op	      15 allocs/op
BenchmarkTcpDelParallel-16      	  450898	      2713 ns/op	      48 B/op	       5 allocs/op
BenchmarkTcpCloneParallel-16    	  411650	      3157 ns/op	     169 B/op	      16 allocs/op
```

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

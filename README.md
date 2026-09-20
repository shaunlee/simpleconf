# simpleconf

`simpleconf` is a lightweight configuration service that can be operated over HTTP/TCP, with support for:

- JSON key-path read/write (for example `product.name`)
- Local append-only persistence
- Optional Raft cluster mode (no single point of failure)

## Benchmarks

All numbers below were measured on the same machine (AMD Ryzen 9 5900HX, 16 logical cores,
Linux). Client and server share the same CPUs, so these are conservative.

### In-memory operations

Pure function calls against the in-memory JSON document — no syscalls, no network.

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkGet-16      	35865764	       31.97 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-16      	 7825952	       153.1 ns/op	      96 B/op	       3 allocs/op
BenchmarkDel-16      	12272269	       96.00 ns/op	      80 B/op	       3 allocs/op
BenchmarkClone-16    	 5811598	       205.7 ns/op	     168 B/op	       4 allocs/op
```

### TCP protocol

`go test ./server/ -bench Tcp`. The serial benchmarks use a single connection with a strict
request → response ping-pong, so they measure **round-trip latency**, not throughput. The
`*Parallel` variants use one connection per goroutine.

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpGet-16              	  119824	     19848 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpSet-16              	  117442	     20140 ns/op	     176 B/op	      11 allocs/op
BenchmarkTcpDel-16              	  122374	     19473 ns/op	     104 B/op	       7 allocs/op
BenchmarkTcpClone-16            	  121299	     20037 ns/op	     216 B/op	      10 allocs/op

BenchmarkTcpGetParallel-16      	  884726	      2550 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpSetParallel-16      	  954890	      2504 ns/op	     227 B/op	      12 allocs/op
BenchmarkTcpDelParallel-16      	  920187	      2711 ns/op	     128 B/op	       7 allocs/op
BenchmarkTcpCloneParallel-16    	  842974	      2714 ns/op	     201 B/op	      11 allocs/op
```

Concurrency scaling for GET (`-cpu 1,2,4,8,16,64,256`). Note that `-cpu 1` reproduces the
serial number exactly, and that allocations per op stay flat — the extra time in the serial
case is syscalls and goroutine scheduling, not the database.

```text
BenchmarkTcpGetParallel         	  120766	     19324 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-2       	  212628	     10993 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-4       	  375663	      6485 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-8       	  640900	      3767 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-16      	  877633	      2632 ns/op	      64 B/op	       8 allocs/op
BenchmarkTcpGetParallel-64      	 1000000	      2272 ns/op	      65 B/op	       8 allocs/op
BenchmarkTcpGetParallel-256     	  686966	      3364 ns/op	      70 B/op	       8 allocs/op
```

TCP load test (historical, `go run ./cmd/bench`):

```text
Running 10s GET test @ 127.0.0.1:23466
  500 connections
  Stats		Avg		Min		Max
  Latency	659.187µs	13.841777ms	17.172µs
  4928439 requests in 10.000008278s
Requests/sec: 492843.59

Running 10s SET test @ 127.0.0.1:23466
  100 connections
  Stats		Avg		Min		Max
  Latency	300.293µs	2.8153ms	18.214µs
  2874593 requests in 10.000167975s
Requests/sec: 287454.47

Running 10s DELETE test @ 127.0.0.1:23466
  100 connections
  Stats		Avg		Min		Max
  Latency	237.16µs	3.515856ms	17.332µs
  3393076 requests in 10.000016297s
Requests/sec: 339307.05
```

### HTTP

`wrk -t4 -d10s` against `http://127.0.0.1:23556/db/bench`. With only 10 connections the
client, not the server, is the bottleneck; raising it to 200 gives roughly 1.5–1.8x the
throughput.

10 connections:

```text
Running 10s GET test @ http://127.0.0.1:23556/db/bench
  4 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    41.42us   27.25us    2.29ms   97.63%
    Req/Sec    45.64k     3.60k   49.78k    80.69%
  1834071 requests in 10.10s, 197.65MB read
Requests/sec: 181601.69
Transfer/sec:     19.57MB

Running 10s SET test @ http://127.0.0.1:23556/db/bench
  4 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    59.03us   65.95us    4.62ms   98.23%
    Req/Sec    33.66k     4.17k   39.86k    70.05%
  1352492 requests in 10.10s, 180.58MB read
Requests/sec: 133910.28
Transfer/sec:     17.88MB

Running 10s DELETE test @ http://127.0.0.1:23556/db/bench
  4 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    50.37us   37.11us    3.05ms   98.15%
    Req/Sec    38.21k     1.95k   42.11k    70.30%
  1535204 requests in 10.10s, 204.97MB read
Requests/sec: 152003.32
Transfer/sec:     20.29MB
```

200 connections:

```text
Running 10s GET test @ http://127.0.0.1:23556/db/bench
  4 threads and 200 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency   429.73us  197.50us    7.07ms   80.69%
    Req/Sec    66.40k     1.90k   73.02k    67.00%
  2642864 requests in 10.03s, 269.69MB read
Requests/sec: 263504.53
Transfer/sec:     26.89MB

Running 10s SET test @ http://127.0.0.1:23556/db/bench
  4 threads and 200 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency   529.52us  473.32us   15.81ms   93.63%
    Req/Sec    60.36k     2.38k   68.74k    70.75%
  2402580 requests in 10.03s, 320.78MB read
Requests/sec: 239501.39
Transfer/sec:     31.98MB

Running 10s DELETE test @ http://127.0.0.1:23556/db/bench
  4 threads and 200 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency   503.34us  405.45us   10.73ms   92.91%
    Req/Sec    61.53k     2.58k   68.32k    64.50%
  2448629 requests in 10.03s, 326.93MB read
Requests/sec: 244092.76
Transfer/sec:     32.59MB
```

HTTP tops out lower than the raw TCP protocol mainly because of response size: the wrk runs
average ~105 bytes per response (status line, `Date`, `Content-Length`, `Content-Type`),
while the TCP GET reply is `$6\n"mark"\n` — 10 bytes.

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
- `listen`: HTTP listen address
- `tcp.listen`: TCP listen address (TCP is disabled if not set)
- `raft.enabled`: enable/disable Raft
- `raft.forward`: whether followers auto-forward writes to leader (default `true`)
- `raft.node_id`: Raft node ID
- `raft.listen`: Raft transport address
- `raft.http_addr`: HTTP address reachable by other nodes/clients
- `raft.bootstrap`: set `true` only on the first node during initial bootstrap
- `raft.peers`: format `id,raft_addr,http_addr`

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
curl -s -X PUT http://127.0.0.1:23456/db/product.name -d '"Demo"'
curl -s -X PUT http://127.0.0.1:23456/db/product.year -d '2026'
curl -s http://127.0.0.1:23456/db/product
curl -s -X DELETE http://127.0.0.1:23456/db/product.year
curl -s -X POST http://127.0.0.1:23456/clone/product.name/product.alias
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

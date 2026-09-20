# simpleconf

`simpleconf` is a lightweight configuration service that can be operated over HTTP/TCP, with support for:

- JSON key-path read/write (for example `product.name`)
- Local append-only persistence
- Optional Raft cluster mode (no single point of failure)

## Benchmarks (Historical)

```text
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkGet-16      	35865764	       31.97 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-16      	 7825952	       153.1 ns/op	      96 B/op	       3 allocs/op
BenchmarkDel-16      	12272269	       96.00 ns/op	      80 B/op	       3 allocs/op
BenchmarkClone-16    	 5811598	       205.7 ns/op	     168 B/op	       4 allocs/op

cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpSet-16      	   51232	     22851 ns/op
BenchmarkTcpGet-16      	   61987	     19295 ns/op
BenchmarkTcpClone-16    	   53688	     22532 ns/op
BenchmarkTcpDel-16      	   54684	     22070 ns/op
```

TCP load test (historical):

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

wrk (historical):

```text
Running 10s GET test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    40.06us   10.91us 738.00us   76.41%
    Req/Sec   104.03k     4.36k  113.08k    65.35%
  2091039 requests in 10.10s, 295.14MB read
Requests/sec: 207040.39
Transfer/sec:     29.22MB

Running 10s SET test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    66.40us   34.08us   1.21ms   90.89%
    Req/Sec    72.18k     1.03k   75.19k    68.81%
  1450633 requests in 10.10s, 172.93MB read
Requests/sec: 143629.05
Transfer/sec:     17.12MB

Running 10s DELETE test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    53.39us   41.82us   3.48ms   96.62%
    Req/Sec    86.50k     2.24k   95.54k    69.00%
  1721124 requests in 10.00s, 205.17MB read
Requests/sec: 172110.01
Transfer/sec:     20.52MB
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

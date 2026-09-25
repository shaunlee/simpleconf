# simpleconf

[![test](https://github.com/shaunlee/simpleconf/actions/workflows/test.yml/badge.svg)](https://github.com/shaunlee/simpleconf/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/shaunlee/simpleconf/graph/badge.svg)](https://codecov.io/gh/shaunlee/simpleconf)
[![Go Reference](https://pkg.go.dev/badge/github.com/shaunlee/simpleconf.svg)](https://pkg.go.dev/github.com/shaunlee/simpleconf)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

simpleconf is a configuration server that holds a single JSON document and
lets clients read and write paths inside it, such as `product.name`, over HTTP
or a line-based TCP protocol. Writes go to an append-only file. Several nodes
can run as a Raft cluster.

It is meant for the small, shared settings a handful of services read often
and change occasionally: feature flags, endpoints, limits. If you need
watches, per-key access control, TLS, or data that does not comfortably fit
in memory, use etcd or Consul instead; simpleconf has none of those.

## Quick start

Requires Go 1.25 or later.

```bash
go install github.com/shaunlee/simpleconf/cmd/simpleconf@latest
DB_DIR=./data simpleconf
```

HTTP listens on `:23456`. Write a value, then read it back:

```bash
curl -X PUT localhost:23456/db/product.name -d '"Demo"'
curl localhost:23456/db/product          # {"name":"Demo"}
curl localhost:23456/db                  # the whole document
```

With Docker, data lives in the `/data` volume:

```bash
docker run -p 23456:23456 -v simpleconf-data:/data shonhen/simpleconf
```

`make docker` builds the same image from a checkout.

## Performance

A keyed read of the in-memory document takes about 10 ns and a write about
30 ns, whatever the document's size. Over the network, on one Apple M6 inside
a Linux VM, a single key serves about 1M GET/s over HTTP with 200 connections,
and 800k GET/s over TCP with 50 connections, rising to 16.5M GET/s when each
connection pipelines 64 commands.

![HTTP API throughput: simpleconf, Consul, etcd](docs/images/bench-http.svg)

![Native protocol throughput: simpleconf TCP, Valkey RESP](docs/images/bench-native.svg)

These comparisons favour simpleconf in ways that have nothing to do with its
code: by default it fsyncs once a second, while etcd and Consul fsync every
write before replying. [docs/benchmarks.md](docs/benchmarks.md) has the setup,
the caveats and the full tables.

## Key paths

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

`#` and `?` cannot be used over HTTP. The key is taken from the URL path
without percent-decoding, so `%23` stays `%23` rather than becoming `#`, and
an unencoded `#` or `?` is consumed by the client as a fragment or query
string. Queries that use them work over TCP only; `*` works over both.

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

A value is stored as the JSON text the client sent, so an integer such as
`9007199254740993` comes back with every digit.

## HTTP API

| Method and path | Does | Success |
| --- | --- | --- |
| `GET /` | the node's role and the version; the role is `leader`, `follower` or `candidate` under Raft, `peer` in peers mode, `standalone` otherwise | `200` |
| `GET /db` | the whole document | `200` |
| `GET /db/:key` | the value at a key path; an empty body if there is none | `200` |
| `PUT /db/:key` | set the value at a key path; the body is raw JSON | `202 {"ok":true}` |
| `DELETE /db/:key` | delete a key path | `202 {"ok":true}` |
| `POST /clone/:from/:to` | copy one key path to another | `202 {"ok":true}` |
| `POST /vacuum` | rewrite the append-only file as one snapshot | `202 {"ok":true}` |

Writes can fail with `422` for a body that is not valid JSON, `400` for a path
that cannot be written (such as a non-numeric index into an array), `503`
while the append-only file cannot be written (see [Durability](#durability)),
and, in a Raft cluster, `409` from a follower that does not forward writes.
Each error body is `{"error":"..."}`.

## TCP protocol

Set `tcp.listen` to enable it. Each command is one line; set and clone take a
second line.

| Command | Request | Reply |
| --- | --- | --- |
| whole document | `=` | `$<length>` line, then the document |
| get | `=key.path` | `$<length>` line, then the value (`$0` and an empty line if there is none) |
| set | `+key.path`, then the JSON value on the next line | `+OK` |
| delete | `-key.path` | `+OK` |
| clone | `<from.path`, then `>to.path` on the next line | `+OK` |
| vacuum | `*` | `+OK` |
| ping | `PING` | `+PONG` |

A failed command replies `-ERR <message>`, and a follower that does not forward
writes replies `-ERR not leader <leader http address>`.

```text
+product.name
"Demo"
+OK
=product.name
$6
"Demo"
```

Replies are flushed only when the server has no further command buffered, so a
client that sends several commands before reading gets their replies in fewer
writes.

## Configuration

The server reads `configs/config.yml` from its working directory. The file is
optional:

```yaml
db:
  dir: data           # default /data
  fsync: everysec     # always | everysec | no
  backups: 3          # old append-only files kept after a vacuum
listen: :23456        # HTTP
tcp:
  listen: :23466      # TCP; off when unset
```

The environment variables `LISTEN`, `TCP_LISTEN` and `DB_DIR` override the
matching settings. `PEERS_LISTEN` is used only when the file sets no
`peers.listen`.

| Key | Meaning |
| --- | --- |
| `db.dir` | data directory |
| `db.fsync` | when to fsync the append-only file; see below |
| `db.backups` | how many previous append-only files a vacuum keeps; `-1` keeps all |
| `listen` | HTTP listen address |
| `tcp.listen` | TCP listen address |
| `raft.*` | see [Raft cluster](#raft-cluster) |
| `peers.*` | see [Peers mode](#peers-mode) |

There is no authentication and no TLS. Keep the ports on a private network, or
put a proxy in front.

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
| Linux VM volume (the benchmarks) | 189k/s | 661k/s |
| macOS APFS/NVMe, native | 24.6k/s | 186k/s |
| ext4/NVMe, previous machine | 19.5k/s | 237k/s |

A virtual disk makes fsync far cheaper than a physical one, so take the 3.5x
ratio in the first row as the floor and the ~10x in the other two as what to
expect on real hardware.

`everysec` and `no` both survive `kill -9`, because the writer flushes each
batch to the kernel before going idle; they differ only in exposure to power
loss.

When the disk fails, simpleconf does what Redis and PostgreSQL do rather than
undo writes in memory:

- A write to the file fails (a full disk, say): under `always` the server
  exits without answering the writers still waiting, and a restart reloads the
  file, so nothing that was not stored survives. Under `everysec` and `no` it
  cuts the partial record off the file and refuses writes, with HTTP `503` or
  TCP `-ERR`, while reads carry on; it retries every second and accepts writes
  again once the pending records are stored. A vacuum that succeeds also ends
  the refusal.
- An fsync fails: the server exits under `always` and `everysec`. After a
  failed fsync the kernel may already have dropped the unwritten data, so a
  later fsync that succeeds proves nothing.

Run it under a supervisor that restarts it, such as systemd or Docker's
`--restart`. If the disk is still broken, the restart fails too, so the fault
cannot go unnoticed.

A vacuum, and every clean shutdown, rewrites the file as one snapshot. The
snapshot is written to a temporary file and renamed into place, and the
previous file is kept alongside it with a timestamp suffix. `db.backups` sets
how many of these are kept, 3 by default: the oldest go first, `0` keeps none
and `-1` keeps them all.

## Raft cluster

Each node needs its own `raft.node_id`, `raft.listen` and data directory, and
every node lists all of them in `raft.peers` as `id,raft_addr,http_addr`. Set
`raft.bootstrap: true` on one node, for the first start of the cluster only.
Three nodes on 10.0.0.1–3, as configured on the first:

```yaml
db:
  dir: /var/lib/simpleconf
listen: :23456
raft:
  enabled: true
  node_id: node-1
  listen: 10.0.0.1:7001               # an address the other nodes can reach, not 0.0.0.0
  http_addr: http://10.0.0.1:23456    # where other nodes send forwarded writes
  bootstrap: true
  fsync: always                       # or everysec; see below
  peers:
    - node-1,10.0.0.1:7001,http://10.0.0.1:23456
    - node-2,10.0.0.2:7001,http://10.0.0.2:23456
    - node-3,10.0.0.3:7001,http://10.0.0.3:23456
```

The addresses can be hostnames, as long as every node can resolve and reach
them. Three nodes in Docker Compose:

```yaml
# compose.yaml
services:
  node1:
    image: shonhen/simpleconf
    hostname: node1
    ports: ["23451:23456"]
    volumes: ["./node1.yml:/configs/config.yml:ro", "node1:/data"]
  node2:
    image: shonhen/simpleconf
    hostname: node2
    ports: ["23452:23456"]
    volumes: ["./node2.yml:/configs/config.yml:ro", "node2:/data"]
  node3:
    image: shonhen/simpleconf
    hostname: node3
    ports: ["23453:23456"]
    volumes: ["./node3.yml:/configs/config.yml:ro", "node3:/data"]

volumes:
  node1:
  node2:
  node3:
```

```yaml
# node1.yml
db:
  dir: /data
listen: :23456
raft:
  enabled: true
  node_id: node1
  listen: node1:7001
  http_addr: http://node1:23456
  bootstrap: true
  peers:
    - node1,node1:7001,http://node1:23456
    - node2,node2:7001,http://node2:23456
    - node3,node3:7001,http://node3:23456
```

`node2.yml` and `node3.yml` are the same with their own `node_id`, `listen`
and `http_addr`, and without `bootstrap`. After `docker compose up -d`, a write
to any node reaches all three; followers apply it a moment after
the reply:

```bash
curl -X PUT localhost:23452/db/app.name -d '"demo"'
sleep 1
curl localhost:23453/db/app          # {"name":"demo"}
```

Writes are committed through the leader. A follower passes a write on to the
leader's HTTP address by default; with `raft.forward: false` it refuses the
write and names the leader instead. Reads are answered by the node that
receives them, so a follower can briefly return a value the leader has already
replaced.

With Raft enabled the append-only file is not used, and `db.fsync` does not
apply: the Raft log and its snapshots, under `db.dir/raft`, hold the data.
`raft.fsync` decides when a node's log entries reach its disk:

| `raft.fsync` | A write is acknowledged once | It can still be lost if |
| --- | --- | --- |
| `always` (default) | a majority of nodes have fsynced it | a majority of the disks fail |
| `everysec` | a majority of nodes have it; each fsyncs within a second | a majority lose power in the same second, or a follower that had it loses power and the leader fails before the write reaches another node |

Under both settings a node fsyncs its term and vote every time they change:
a node that forgot its vote could vote twice in one term and let two leaders
be elected. Choose `everysec` only when the nodes do not share a power supply.

Concurrent writes share an fsync, so under `always` a single client writing
one key at a time sees the full cost of the disk. On one test machine a single
writer took 590 µs per write under `always` and 40 µs under `everysec`; see
[docs/benchmarks.md](docs/benchmarks.md#raft).

## Peers mode

Without Raft, nodes can also copy each other's writes. This is simpler and
weaker than Raft, and is kept for existing deployments.

```yaml
peers:
  listen: :23457                        # accepts writes from other nodes
  addresses:                            # the other nodes' peers.listen
    - http://10.0.0.2:23457
    - http://10.0.0.3:23457
```

- On start, a node with an empty document, such as a new node or one whose
  data directory was replaced, copies the document from the first peer that
  answers. A node that already has data keeps it, and gets what it missed
  while it was down from the other nodes' queues. If a queue was lost, for
  example in a crash under `db.fsync: everysec`, the nodes stay different:
  stop the node that is behind, delete its `db.dir`, and start it again to
  copy the whole document.
- Each write is queued under `db.dir/peers-wal` before the client gets its
  reply, and sent to every peer in the background, in order. The queue is
  fsynced as `db.fsync` says, like the local data. A peer that cannot be
  reached is retried until it comes back. Its queue has no limit, so while it
  is down every write adds its path and body to the queue, on disk and in
  memory. A write a peer rejects as invalid is dropped.
- Nothing resolves conflicts. Two nodes that change the same key at about the
  same time can end up with different values.

## License

[MIT](LICENSE)

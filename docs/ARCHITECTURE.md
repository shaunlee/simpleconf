# Architecture Overview

## Runtime paths

- Read requests (`GET /db*`, TCP `=`) are served from local in-memory state.
- Write requests (`PUT/DELETE/clone/vacuum`, TCP `+/-/</*`) go through `cluster.Apply*`.

## Cluster mode

- When `raft.enabled=true`, nodes form a Raft cluster.
- Leader applies commands through Raft log replication.
- Followers optionally forward write requests to leader when `raft.forward=true`. Node-to-node traffic uses only the Raft port: a connection that opens with the byte `F` carries forwarded writes (`internal/cluster/forward.go`), any other is hashicorp/raft's. A leader from v0.6 or earlier closes such a connection, and the follower then falls back to the leader's HTTP address if one is configured.

## Non-Raft mode

- When `raft.enabled=false`, the service can use legacy `peers` sync behavior. Queued writes go to each peer in batches over the peers protocol (`internal/peers/proto.go`), on the peers port: a connection that opens with `SCP1\r\n` speaks it, any other is HTTP, kept for peers from v0.7 and earlier. A sender whose `SCP1\r\n` gets an HTTP reply uses HTTP with that peer for 30 seconds before trying again.
- `internal/wire` holds what both node-to-node protocols share: length-prefixed frames and the listener that splits a port by first byte.

## Durability

- Without Raft, DB data is persisted by the `internal/db` append-only file, fsynced according to `db.fsync`.
- With Raft, the append-only file is off. The Raft log, term and vote are persisted by `internal/cluster` (`fileStore`) under `db.dir/raft`. Term and vote are fsynced on every change; log entries on every write under `raft.fsync: always` (the default), or by a background loop once a second under `everysec`. Raft's own snapshots of the document sit beside them. Raft checks every 10 to 20 seconds whether a snapshot is due; after one it drops old log entries, and only then does `fileStore` rewrite its checkpoint and empty its WAL, so between snapshots it only appends.

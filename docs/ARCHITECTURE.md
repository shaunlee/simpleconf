# Architecture Overview

## Runtime paths

- Read requests (`GET /db*`, TCP `=`) are served from local in-memory state.
- Write requests (`PUT/DELETE/clone/vacuum`, TCP `+/-/</*`) go through `cluster.Apply*`.

## Cluster mode

- When `raft.enabled=true`, nodes form a Raft cluster.
- Leader applies commands through Raft log replication.
- Followers optionally forward write requests to leader when `raft.forward=true`.

## Non-Raft mode

- When `raft.enabled=false`, the service can use legacy `peers` sync behavior.

## Durability

- Without Raft, DB data is persisted by the `internal/db` append-only file, fsynced according to `db.fsync`.
- With Raft, the append-only file is off. The Raft log, term and vote are persisted by `internal/cluster` (`fileStore`) under `db.dir/raft`. Term and vote are fsynced on every change; log entries on every write under `raft.fsync: always` (the default), or by a background loop once a second under `everysec`. Raft's own snapshots of the document sit beside them. Raft checks every 10 to 20 seconds whether a snapshot is due; after one it drops old log entries, and only then does `fileStore` rewrite its checkpoint and empty its WAL, so between snapshots it only appends.

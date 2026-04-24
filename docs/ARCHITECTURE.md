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

- DB data is persisted by the `db` package append-only file.
- Raft metadata/log state is persisted by `cluster/fileStore` under `db.dir/raft`.

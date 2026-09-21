# Project Structure

- `cmd/simpleconf`: the service entrypoint; wires configuration to the packages below.
- `cmd/simpleconf-bench`: TCP load generator.
- `internal/config`: loads `configs/config.yml` and environment overrides.
- `internal/db`: in-memory document tree and append-only file.
- `internal/cluster`: Raft coordination; every write goes through `cluster.Apply*`.
- `internal/httpapi`: HTTP API (Fiber).
- `internal/tcpapi`: line-based TCP protocol.
- `internal/peers`: legacy peer sync, used when Raft is disabled.
- `configs/examples`: runnable configuration examples.
- `docker`: container packaging files.
- `docs`: architecture and repository documentation.

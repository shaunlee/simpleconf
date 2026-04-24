# Project Structure

- `cmd/bin`: main application entrypoint.
- `cmd/bench`: benchmark/utility entrypoint.
- `actions`: HTTP API handlers (Fiber).
- `server`: TCP protocol server.
- `cluster`: Raft coordination and persistence abstractions.
- `db`: local append-only configuration storage engine.
- `peers`: legacy peer sync mode (used when Raft is disabled).
- `configs/examples`: runnable configuration examples.
- `docker`: container packaging files.
- `docs`: architecture and repository documentation.

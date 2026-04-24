# 3-Node Raft Example

Use the same `raft.peers` list on all nodes:

- `node-1,10.0.0.11:7001,http://10.0.0.11:23456`
- `node-2,10.0.0.12:7001,http://10.0.0.12:23456`
- `node-3,10.0.0.13:7001,http://10.0.0.13:23456`

Node-specific fields:

- `raft.node_id`
- `raft.listen`
- `raft.http_addr`
- `listen`
- `db.dir`

Bootstrap rule:

- Only first node uses `raft.bootstrap=true` on first cluster bring-up.
- Other nodes use `raft.bootstrap=false`.

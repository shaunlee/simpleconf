package cluster

import (
	"bytes"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/hashicorp/raft"
	"github.com/shaunlee/simpleconf/db"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const applyTimeout = 3 * time.Second

type Peer struct {
	ID       string
	RaftAddr string
	HTTPAddr string
}

type Config struct {
	Enabled   bool
	Forward   bool
	NodeID    string
	RaftAddr  string
	HTTPAddr  string
	Dir       string
	Bootstrap bool
	Peers     []Peer
}

type command struct {
	Op      string `json:"op"`
	Key     string `json:"key,omitempty"`
	Value   any    `json:"value,omitempty"`
	FromKey string `json:"from_key,omitempty"`
	ToKey   string `json:"to_key,omitempty"`
}

type NotLeaderError struct {
	LeaderHTTPAddr string
}

func (e *NotLeaderError) Error() string {
	if len(e.LeaderHTTPAddr) == 0 {
		return "not leader"
	}
	return "not leader, leader=" + e.LeaderHTTPAddr
}

type Manager struct {
	enabled bool
	forward bool

	mu         sync.RWMutex
	raft       *raft.Raft
	raftAddr   string
	httpAddr   string
	raftToHTTP map[string]string
}

var (
	defaultMu      sync.RWMutex
	defaultManager = &Manager{}
)

func SetDefault(m *Manager) {
	defaultMu.Lock()
	defaultManager = m
	defaultMu.Unlock()
}

func getDefault() *Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultManager
}

func AsNotLeader(err error) (*NotLeaderError, bool) {
	nl, ok := err.(*NotLeaderError)
	return nl, ok
}

func Start(cfg Config) (*Manager, error) {
	m := &Manager{
		enabled: cfg.Enabled,
		forward: cfg.Forward,
	}
	if !cfg.Enabled {
		return m, nil
	}
	if len(cfg.NodeID) == 0 || len(cfg.RaftAddr) == 0 || len(cfg.Dir) == 0 {
		return nil, fmt.Errorf("raft enabled but missing required config: node_id/raft_addr/dir")
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}

	raftCfg := raft.DefaultConfig()
	raftCfg.LocalID = raft.ServerID(cfg.NodeID)
	raftCfg.LogLevel = "WARN"

	addr, err := net.ResolveTCPAddr("tcp", cfg.RaftAddr)
	if err != nil {
		return nil, err
	}
	transport, err := raft.NewTCPTransport(cfg.RaftAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, err
	}

	store, err := newFileStore(cfg.Dir)
	if err != nil {
		return nil, err
	}
	snapshots, err := raft.NewFileSnapshotStore(cfg.Dir, 2, os.Stderr)
	if err != nil {
		return nil, err
	}

	r, err := raft.NewRaft(raftCfg, &fsm{}, store, store, snapshots, transport)
	if err != nil {
		return nil, err
	}

	if cfg.Bootstrap && len(cfg.Peers) > 0 {
		servers := make([]raft.Server, 0, len(cfg.Peers))
		for _, p := range cfg.Peers {
			servers = append(servers, raft.Server{
				ID:      raft.ServerID(p.ID),
				Address: raft.ServerAddress(p.RaftAddr),
			})
		}
		if f := r.BootstrapCluster(raft.Configuration{Servers: servers}); f.Error() != nil && f.Error() != raft.ErrCantBootstrap {
			return nil, f.Error()
		}
	}

	raftToHTTP := make(map[string]string, len(cfg.Peers)+1)
	for _, p := range cfg.Peers {
		if len(p.RaftAddr) > 0 && len(p.HTTPAddr) > 0 {
			raftToHTTP[p.RaftAddr] = normalizeHTTPAddr(p.HTTPAddr)
		}
	}
	if len(cfg.RaftAddr) > 0 && len(cfg.HTTPAddr) > 0 {
		raftToHTTP[cfg.RaftAddr] = normalizeHTTPAddr(cfg.HTTPAddr)
	}

	m.raft = r
	m.raftAddr = cfg.RaftAddr
	m.httpAddr = normalizeHTTPAddr(cfg.HTTPAddr)
	m.raftToHTTP = raftToHTTP
	log.Println("raft enabled on", cfg.RaftAddr, "node", cfg.NodeID, "forward", cfg.Forward)
	return m, nil
}

func (m *Manager) Shutdown() {
	if !m.enabled || m.raft == nil {
		return
	}
	_ = m.raft.Shutdown().Error()
}

func (m *Manager) ApplySet(key string, value any) error {
	return m.apply(command{Op: "set", Key: key, Value: value})
}

func (m *Manager) ApplyDelete(key string) error {
	return m.apply(command{Op: "del", Key: key})
}

func (m *Manager) ApplyClone(fromKey, toKey string) error {
	return m.apply(command{Op: "clone", FromKey: fromKey, ToKey: toKey})
}

func (m *Manager) ApplyVacuum() error {
	return m.apply(command{Op: "vacuum"})
}

func ApplySet(key string, value any) error {
	return getDefault().ApplySet(key, value)
}

func ApplyDelete(key string) error {
	return getDefault().ApplyDelete(key)
}

func ApplyClone(fromKey, toKey string) error {
	return getDefault().ApplyClone(fromKey, toKey)
}

func ApplyVacuum() error {
	return getDefault().ApplyVacuum()
}

func (m *Manager) apply(c command) error {
	if !m.enabled || m.raft == nil {
		return applyLocal(c)
	}
	if m.raft.State() != raft.Leader {
		leader := m.leaderHTTPAddr()
		if !m.forward || len(leader) == 0 {
			return &NotLeaderError{LeaderHTTPAddr: leader}
		}
		return m.forwardToLeader(c, leader)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f := m.raft.Apply(b, applyTimeout)
	return f.Error()
}

func (m *Manager) leaderHTTPAddr() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.raft == nil {
		return ""
	}
	leaderRaftAddr := string(m.raft.Leader())
	if len(leaderRaftAddr) == 0 {
		return ""
	}
	if leaderRaftAddr == m.raftAddr {
		return m.httpAddr
	}
	return m.raftToHTTP[leaderRaftAddr]
}

func (m *Manager) forwardToLeader(c command, leader string) error {
	var (
		method string
		path   string
		body   []byte
	)
	switch c.Op {
	case "set":
		method = http.MethodPut
		path = "/db/" + c.Key
		b, err := json.Marshal(c.Value)
		if err != nil {
			return err
		}
		body = b
	case "del":
		method = http.MethodDelete
		path = "/db/" + c.Key
	case "clone":
		method = http.MethodPost
		path = "/clone/" + c.FromKey + "/" + c.ToKey
	case "vacuum":
		method = http.MethodPost
		path = "/vacuum"
	default:
		return fmt.Errorf("unknown op: %s", c.Op)
	}

	req, err := http.NewRequest(method, leader+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: applyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusConflict {
		var v struct {
			Leader string `json:"leader"`
		}
		if err := json.Unmarshal(respBody, &v); err == nil && len(v.Leader) > 0 {
			return &NotLeaderError{LeaderHTTPAddr: v.Leader}
		}
		return &NotLeaderError{LeaderHTTPAddr: leader}
	}
	return fmt.Errorf("leader forward failed: status=%d body=%s", resp.StatusCode, string(respBody))
}

func normalizeHTTPAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if len(addr) == 0 {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

type fsm struct{}

func (f *fsm) Apply(logEntry *raft.Log) any {
	var c command
	if err := json.Unmarshal(logEntry.Data, &c); err != nil {
		return err
	}
	return applyLocal(c)
}

func applyLocal(c command) error {
	switch c.Op {
	case "set":
		return db.Set(c.Key, c.Value)
	case "del":
		db.Del(c.Key)
		return nil
	case "clone":
		db.Clone(c.FromKey, c.ToKey)
		return nil
	case "vacuum":
		db.Vacuum()
		return nil
	default:
		return fmt.Errorf("unknown op: %s", c.Op)
	}
}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	return &snapshot{state: db.Get("")}, nil
}

func (f *fsm) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	buf, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	return db.Replace(string(buf))
}

type snapshot struct {
	state string
}

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write([]byte(s.state)); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *snapshot) Release() {}

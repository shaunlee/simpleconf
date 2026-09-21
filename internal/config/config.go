// Package config loads the service configuration from configs/config.yml and
// the environment.
package config

import (
	"log"
	"strings"

	"github.com/shaunlee/simpleconf/internal/cluster"
	"github.com/spf13/viper"
)

type Config struct {
	Listen    string
	TCPListen string
	DBDir     string
	Fsync     string

	Raft cluster.Config

	PeerAddrs  []string
	PeerListen string
}

// Load reads configs/config.yml when present; a missing file is not an error.
// Every key can be overridden from the environment. db_dir and tcp_listen take
// precedence over db.dir and tcp.listen; peers.listen over peers_listen.
func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath("configs")
	v.SetConfigType("yaml")
	v.SetDefault("listen", ":23456")
	v.SetDefault("raft.forward", true)
	v.AutomaticEnv()
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	c := &Config{
		Listen:    v.GetString("listen"),
		TCPListen: first(v.GetString("tcp_listen"), v.GetString("tcp.listen")),
		DBDir:     first(v.GetString("db_dir"), v.GetString("db.dir"), "/data"),
		Fsync:     v.GetString("db.fsync"),
		Raft: cluster.Config{
			Enabled:   v.GetBool("raft.enabled"),
			Forward:   v.GetBool("raft.forward"),
			NodeID:    v.GetString("raft.node_id"),
			RaftAddr:  v.GetString("raft.listen"),
			HTTPAddr:  v.GetString("raft.http_addr"),
			Bootstrap: v.GetBool("raft.bootstrap"),
			Peers:     parseRaftPeers(v.GetStringSlice("raft.peers")),
		},
		PeerAddrs:  v.GetStringSlice("peers.addresses"),
		PeerListen: first(v.GetString("peers.listen"), v.GetString("peers_listen")),
	}
	return c, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if len(v) > 0 {
			return v
		}
	}
	return ""
}

func parseRaftPeers(raw []string) []cluster.Peer {
	peers := make([]cluster.Peer, 0, len(raw))
	for _, row := range raw {
		row = strings.TrimSpace(row)
		if len(row) == 0 {
			continue
		}
		parts := strings.Split(row, ",")
		if len(parts) < 3 {
			log.Printf("ignored invalid raft peer row %q, expected id,raft_addr,http_addr", row)
			continue
		}
		peers = append(peers, cluster.Peer{
			ID:       strings.TrimSpace(parts[0]),
			RaftAddr: strings.TrimSpace(parts[1]),
			HTTPAddr: strings.TrimSpace(parts[2]),
		})
	}
	return peers
}

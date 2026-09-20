package main

import (
	"github.com/gofiber/fiber/v3"
	"github.com/shaunlee/simpleconf/actions"
	"github.com/shaunlee/simpleconf/cluster"
	"github.com/shaunlee/simpleconf/db"
	"github.com/shaunlee/simpleconf/peers"
	"github.com/shaunlee/simpleconf/server"
	"github.com/spf13/viper"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath("configs")
	viper.SetConfigType("yaml")
	viper.SetDefault("listen", ":23456")
	viper.SetDefault("raft.forward", true)
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Fatalf("config error: %v", err)
		}
	}

	dbdir := viper.GetString("db_dir")
	if len(dbdir) == 0 {
		dbdir = viper.GetString("db.dir")
	}
	if len(dbdir) == 0 {
		dbdir = "/data"
	}

	raftEnabled := viper.GetBool("raft.enabled")
	if raftEnabled {
		db.DisableAOF()
	}

	policy, err := db.ParseFsyncPolicy(viper.GetString("db.fsync"))
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	db.SetFsyncPolicy(policy)

	db.Init(dbdir)
	defer db.Close(true)
	if !raftEnabled {
		peers.SetWALDir(filepath.Join(dbdir, "peers-wal"))
	}

	raftManager, err2 := cluster.Start(cluster.Config{
		Enabled:   raftEnabled,
		Forward:   viper.GetBool("raft.forward"),
		NodeID:    viper.GetString("raft.node_id"),
		RaftAddr:  viper.GetString("raft.listen"),
		HTTPAddr:  viper.GetString("raft.http_addr"),
		Dir:       filepath.Join(dbdir, "raft"),
		Bootstrap: viper.GetBool("raft.bootstrap"),
		Peers:     parseRaftPeers(viper.GetStringSlice("raft.peers")),
	})
	if err2 != nil {
		log.Panic(err2)
	}
	cluster.SetDefault(raftManager)
	defer raftManager.Shutdown()

	if !raftEnabled {
		peerAddrs := viper.GetStringSlice("peers.addresses")
		peers.Restore(peerAddrs)

		peerListenAddr := viper.GetString("peers.listen")
		if len(peerListenAddr) == 0 {
			peerListenAddr = viper.GetString("peers_listen")
		}
		if len(peerListenAddr) > 0 {
			log.Println("peer server listening on", peerListenAddr)
			go peers.Listen(peerListenAddr, peerAddrs)
		}
	}

	app := actions.New()
	go func() {
		if err := app.Listen(viper.GetString("listen"), fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
			log.Panic(err)
		}
	}()
	defer app.Shutdown()

	tcpApp := server.New()
	tcpaddr := viper.GetString("tcp_listen")
	if len(tcpaddr) == 0 {
		tcpaddr = viper.GetString("tcp.listen")
	}
	if len(tcpaddr) > 0 {
		log.Println("tcp server listening on", tcpaddr)
		go func() {
			if err := tcpApp.Listen(tcpaddr); err != nil {
				log.Panic(err)
			}
		}()
	}
	defer tcpApp.Shutdown()

	log.Println("http server listening on", viper.GetString("listen"))
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
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

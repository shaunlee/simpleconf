package main

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gofiber/fiber/v3"
	"github.com/shaunlee/simpleconf/internal/cluster"
	"github.com/shaunlee/simpleconf/internal/config"
	"github.com/shaunlee/simpleconf/internal/db"
	"github.com/shaunlee/simpleconf/internal/httpapi"
	"github.com/shaunlee/simpleconf/internal/peers"
	"github.com/shaunlee/simpleconf/internal/tcpapi"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	if cfg.Raft.Enabled {
		db.DisableAOF()
	}

	policy, err := db.ParseFsyncPolicy(cfg.Fsync)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	db.SetFsyncPolicy(policy)

	db.Init(cfg.DBDir)
	defer db.Close(true)
	if !cfg.Raft.Enabled {
		peers.SetWALDir(filepath.Join(cfg.DBDir, "peers-wal"))
	}

	cfg.Raft.Dir = filepath.Join(cfg.DBDir, "raft")
	raftManager, err := cluster.Start(cfg.Raft)
	if err != nil {
		log.Panic(err)
	}
	cluster.SetDefault(raftManager)
	defer raftManager.Shutdown()

	if !cfg.Raft.Enabled {
		peers.Restore(cfg.PeerAddrs)
		if len(cfg.PeerAddrs) > 0 {
			cluster.SetLocalWriteHook(peers.SyncWrite)
		}

		if len(cfg.PeerListen) > 0 {
			log.Println("peer server listening on", cfg.PeerListen)
			go peers.Listen(cfg.PeerListen, cfg.PeerAddrs)
		}
	}

	app := httpapi.New()
	go func() {
		if err := app.Listen(cfg.Listen, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
			log.Panic(err)
		}
	}()
	defer func() {
		if err := app.Shutdown(); err != nil {
			log.Printf("http shutdown: %v", err)
		}
	}()

	tcpApp := tcpapi.New()
	if len(cfg.TCPListen) > 0 {
		log.Println("tcp server listening on", cfg.TCPListen)
		go func() {
			if err := tcpApp.Listen(cfg.TCPListen); err != nil {
				log.Panic(err)
			}
		}()
	}
	defer tcpApp.Shutdown()

	log.Println("http server listening on", cfg.Listen)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

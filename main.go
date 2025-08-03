package main

import (
	"github.com/shaunlee/simpleconf/actions"
	"github.com/shaunlee/simpleconf/db"
	"github.com/shaunlee/simpleconf/server"
	"github.com/spf13/viper"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.SetDefault("listen", ":23456")
	viper.AutomaticEnv()
	viper.ReadInConfig()

	dbdir := viper.GetString("db_dir")
	if len(dbdir) == 0 {
		dbdir = viper.GetString("db.dir")
	}
	if len(dbdir) == 0 {
		dbdir = "/data"
	}
	db.Init(dbdir)
	defer db.Close(true)

	//peers.Restore(viper.GetStringSlice("peers.addresses"))

	//go peers.Listen(
	//	viper.GetString("peers.listen"),
	//	viper.GetStringSlice("peers.addresses"),
	//)

	app := actions.New()
	go func() {
		if err := app.Listen(viper.GetString("listen")); err != nil {
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

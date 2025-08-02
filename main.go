package main

import (
	"github.com/shaunlee/simpleconf/actions"
	"github.com/shaunlee/simpleconf/db"
	//"github.com/shaunlee/simpleconf/peers"
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
	viper.SetDefault("db.dir", "/data")
	viper.AutomaticEnv()
	viper.ReadInConfig()

	db.Init(viper.GetString("db.dir"))
	defer db.Close(true)

	//peers.Restore(viper.GetStringSlice("peers.addresses"))

	//go peers.Listen(
	//	viper.GetString("peers.listen"),
	//	viper.GetStringSlice("peers.addresses"),
	//)

	app := actions.Route()

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		app.Shutdown()
	}()

	if err := app.Listen(viper.GetString("listen")); err != nil {
		log.Println(err)
	}
}

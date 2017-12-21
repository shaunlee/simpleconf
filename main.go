package main

import (
	"flag"
	"github.com/shaunlee/simpleconf/controllers"
	"github.com/shaunlee/simpleconf/models"
	"github.com/shaunlee/simpleconf/peers"
	"github.com/spf13/viper"
	"log"
)

func main() {
	var ignorePeers bool
	flag.BoolVar(&ignorePeers, "ignore-peers", false, "ignore peers as of first node starting")
	flag.Parse()

	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.ReadInConfig()

	log.Println("init db ...")
	models.InitDb(viper.GetString("db"))
	defer models.FreeDb()

	if err := peers.Restore(viper.GetStringSlice("peers.addresses")); err != nil && !ignorePeers {
		log.Fatal(err)
	}

	go peers.Listen(
		viper.GetString("peers.listen"),
		viper.GetStringSlice("peers.addresses"),
	)

	controllers.Listen(viper.GetString("listen"))
}

package main

import (
	"github.com/shaunlee/simpleconf/controllers"
	"github.com/shaunlee/simpleconf/models"
	"github.com/shaunlee/simpleconf/peers"
	"github.com/spf13/viper"
	"log"
)

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.ReadInConfig()

	log.Println("init db ...")
	models.InitDb(viper.GetString("db"))
	defer models.FreeDb()

	peers.Restore(viper.GetStringSlice("peers.addresses"))

	go peers.Listen(
		viper.GetString("peers.listen"),
		viper.GetStringSlice("peers.addresses"),
	)

	controllers.Listen(viper.GetString("listen"))
}

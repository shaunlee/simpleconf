package main

import (
	"github.com/shaunlee/simpleconf/controllers"
	"github.com/shaunlee/simpleconf/models"
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
	viper.ReadInConfig()

	log.Println("init db ...")
	models.InitDb(viper.GetString("db"))
	defer models.FreeDb()

	//peers.Restore(viper.GetStringSlice("peers.addresses"))

	//go peers.Listen(
	//	viper.GetString("peers.listen"),
	//	viper.GetStringSlice("peers.addresses"),
	//)

	app := controllers.Route()

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

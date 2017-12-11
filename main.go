package main

import (
	"flag"
	"github.com/shaunlee/simpleconf/controllers"
	"github.com/shaunlee/simpleconf/models"
	"log"
)

func main() {
	var dbfile, listen string

	flag.StringVar(&dbfile, "db", "data.aof", "Appendonly database filename")
	flag.StringVar(&listen, "listen", ":3000", "Http server address listen on")
	flag.Parse()

	log.Println("init db ...")
	models.InitDb(dbfile)
	defer models.FreeDb()

	controllers.Route(listen)
}

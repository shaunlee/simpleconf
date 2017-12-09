package main

import (
	"flag"
	"github.com/shaunlee/simpleconf/controllers"
	"github.com/shaunlee/simpleconf/db"
	"log"
)

func main() {
	var dbfile, listen string

	flag.StringVar(&dbfile, "db", "data.aof", "Appendonly database filename")
	flag.StringVar(&listen, "listen", ":3000", "Http server address listen on")
	flag.Parse()

	log.Println("init db ...")
	db.InitDb(dbfile)
	defer db.FreeDb()

	controllers.Route(listen)
}

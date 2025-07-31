package peers

import (
	"github.com/goccy/go-json"
	"github.com/kataras/iris"
	"github.com/shaunlee/simpleconf/models"
)

var (
	peers []string
)

func whole(ctx iris.Context) {
	ctx.ContentType("application/json")
	ctx.WriteString(models.Configuration)
}

func update(ctx iris.Context) {
	var v interface{}
	ctx.ReadJSON(&v)

	models.Set(ctx.Params().Get("key"), v)

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func forget(ctx iris.Context) {
	models.Del(ctx.Params().Get("key"))

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func clone(ctx iris.Context) {
	models.Clone(
		ctx.Params().Get("from_key"),
		ctx.Params().Get("to_key"),
	)

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func rewrite_aof(ctx iris.Context) {
	models.RewriteAof()

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func Listen(addr string, peerAddrs []string) {
	peers = peerAddrs

	app := iris.New()

	app.Get("/db", whole)
	app.Post("/db/{key}", update)
	app.Delete("/db/{key}", forget)
	app.Post("/clone/{from_key}/{to_key}", clone)
	app.Post("/rewriteaof", rewrite_aof)

	app.Run(iris.Addr(addr))
}

package controllers

import (
	"github.com/kataras/iris"
	"github.com/shaunlee/simpleconf/models"
	"github.com/shaunlee/simpleconf/peers"
)

func whole(ctx iris.Context) {
	ctx.ContentType("application/json")
	ctx.WriteString(models.Configuration)
}

func single(ctx iris.Context) {
	ctx.ContentType("application/json")
	ctx.WriteString(models.Get(ctx.Params().Get("key")))
}

func update(ctx iris.Context) {
	var v interface{}
	ctx.ReadJSON(&v)

	k := ctx.Params().Get("key")

	peers.SyncUpdate(k, v)
	models.Set(k, v)

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func forget(ctx iris.Context) {
	k := ctx.Params().Get("key")

	peers.SyncDelete(k)
	models.Del(k)

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func clone(ctx iris.Context) {
	fk := ctx.Params().Get("from_key")
	tk := ctx.Params().Get("to_key")

	peers.SyncClone(fk, tk)
	models.Clone(fk, tk)

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func rewrite_aof(ctx iris.Context) {
	peers.SyncRewriteAof()
	models.RewriteAof()

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func Listen(addr string) {
	app := iris.New()

	app.Get("/db", whole)
	app.Get("/db/{key}", single)
	app.Post("/db/{key}", update)
	app.Delete("/db/{key}", forget)
	app.Post("/clone/{from_key}/{to_key}", clone)
	app.Post("/rewriteaof", rewrite_aof)

	app.Run(iris.Addr(addr))
}

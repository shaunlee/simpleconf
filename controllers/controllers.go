package controllers

import (
	"github.com/kataras/iris"
	"github.com/shaunlee/simpleconf/models"
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

func Route(addr string) {
	app := iris.New()

	app.Get("/db", whole)
	app.Get("/db/{key}", single)
	app.Post("/db/{key}", update)
	app.Delete("/db/{key}", forget)
	app.Post("/clone/{from_key}/{to_key}", clone)

	app.Run(iris.Addr(addr))
}

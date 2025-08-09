package peers

import (
	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/shaunlee/simpleconf/db"
)

var (
	peers []string
)

func whole(c *fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	ctx.WriteString(db.Get(""))
}

func update(c *fiber.Ctx) error {
	var v any
	ctx.ReadJSON(&v)

	db.Set(ctx.Params().Get("key"), v)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func forget(c *fiber.Ctx) error {
	db.Del(ctx.Params().Get("key"))

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func clone(c *fiber.Ctx) error {
	db.Clone(
		ctx.Params().Get("from_key"),
		ctx.Params().Get("to_key"),
	)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func vacuum(c *fiber.Ctx) error {
	db.Vacuum()

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func Listen(addr string, peerAddrs []string) {
	peers = peerAddrs

	app := iris.New()

	app.Get("/db", whole)
	app.Post("/db/{key}", update)
	app.Delete("/db/{key}", forget)
	app.Post("/clone/{from_key}/{to_key}", clone)
	app.Post("/vacuum", vacuum)

	app.Run(iris.Addr(addr))
}

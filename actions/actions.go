package actions

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/shaunlee/simpleconf/db"
	"github.com/shaunlee/simpleconf/utils"
	//"github.com/shaunlee/simpleconf/peers"
)

func whole(c *fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	return c.SendString(db.Configuration)
}

func single(c *fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	return c.SendString(db.Get(c.Params("key")))
}

func update(c *fiber.Ctx) error {
	k := c.Params("key")
	v := utils.Bytes2Any(c.Body())

	//peers.SyncUpdate(k, v)
	db.Set(k, v)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func forget(c *fiber.Ctx) error {
	k := c.Params("key")

	//peers.SyncDelete(k)
	db.Del(k)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func clone(c *fiber.Ctx) error {
	fk := c.Params("from_key")
	tk := c.Params("to_key")

	//peers.SyncClone(fk, tk)
	db.Clone(fk, tk)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func rewriteAof(c *fiber.Ctx) error {
	//peers.SyncRewriteAof()
	db.RewriteAof()

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func Route() *fiber.App {
	app := fiber.New()
	app.Use(recover.New())

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("simpleconf: v0.3.0-beta")
	})

	app.Get("/db", whole)
	app.Get("/db/:key", single)
	app.Post("/db/:key", update)
	app.Delete("/db/:key", forget)
	app.Post("/clone/:from_key/:to_key", clone)
	app.Post("/rewriteaof", rewriteAof)

	return app
}

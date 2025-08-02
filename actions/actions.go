package actions

import (
	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/shaunlee/simpleconf/db"
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
	var v any
	if err := json.Unmarshal(c.Body(), &v); err != nil {
		return c.Status(422).JSON(fiber.Map{"error": err.Error()})
	}
	k := c.Params("key")

	if err := db.Set(k, v); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func forget(c *fiber.Ctx) error {
	k := c.Params("key")

	db.Del(k)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func clone(c *fiber.Ctx) error {
	fk := c.Params("from_key")
	tk := c.Params("to_key")

	db.Clone(fk, tk)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func rewriteAof(c *fiber.Ctx) error {
	db.RewriteAof()

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func Route() *fiber.App {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	app.Use(recover.New())

	app.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"role":    "master",
			"version": "v0.3.0-beta",
		})
	})

	app.Get("/db", whole)
	app.Get("/db/:key", single)
	app.Put("/db/:key", update)
	app.Delete("/db/:key", forget)
	app.Post("/clone/:from_key/:to_key", clone)
	app.Post("/rewriteaof", rewriteAof)

	return app
}

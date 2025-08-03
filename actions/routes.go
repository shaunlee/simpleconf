package actions

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func New() *fiber.App {
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
	app.Post("/vacuum", vacuum)

	return app
}

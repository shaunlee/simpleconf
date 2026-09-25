package httpapi

import (
	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
)

const version = "v0.6.1"

func New() *fiber.App {
	app := fiber.New(fiber.Config{
		JSONEncoder: json.Marshal,
		JSONDecoder: json.Unmarshal,
	})
	app.Use(recover.New())

	app.Get("/", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"role":    "master",
			"version": version,
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

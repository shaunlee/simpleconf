package peers

import (
	"log"
	"sync"

	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/shaunlee/simpleconf/db"
)

var (
	peers   []string
	peersMu sync.RWMutex
)

func whole(c fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	return c.SendString(db.Get(""))
}

func update(c fiber.Ctx) error {
	var v any
	if err := json.Unmarshal(c.Body(), &v); err != nil {
		return c.Status(422).JSON(fiber.Map{"error": err.Error()})
	}
	if err := db.Set(c.Params("key"), v); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func forget(c fiber.Ctx) error {
	db.Del(c.Params("key"))

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func clone(c fiber.Ctx) error {
	db.Clone(
		c.Params("from_key"),
		c.Params("to_key"),
	)

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func vacuum(c fiber.Ctx) error {
	db.Vacuum()

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func Listen(addr string, peerAddrs []string) {
	Configure(peerAddrs)

	app := fiber.New(fiber.Config{
		JSONEncoder: json.Marshal,
		JSONDecoder: json.Unmarshal,
	})
	app.Use(recover.New())

	app.Get("/db", whole)
	app.Put("/db/:key", update)
	app.Delete("/db/:key", forget)
	app.Post("/clone/:from_key/:to_key", clone)
	app.Post("/vacuum", vacuum)

	if err := app.Listen(addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		log.Panic(err)
	}
}

func Configure(peerAddrs []string) {
	copied := append([]string(nil), peerAddrs...)

	peersMu.Lock()
	peers = copied
	peersMu.Unlock()

	ensureWorkers(copied)
}

func peerAddresses() []string {
	peersMu.RLock()
	defer peersMu.RUnlock()
	return append([]string(nil), peers...)
}

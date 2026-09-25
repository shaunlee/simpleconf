package peers

import (
	"errors"
	"log"
	"net"
	"sync"

	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/shaunlee/simpleconf/internal/db"
	"github.com/shaunlee/simpleconf/internal/wire"
)

var (
	peers   []string
	peersMu sync.RWMutex
)

func whole(c fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	return c.SendString(db.Get(""))
}

// update stores the body as sent, so a replicated value keeps its text,
// including integers too large for a float64.
func update(c fiber.Ctx) error {
	return reply(c, db.SetRaw(c.Params("key"), c.Body()))
}

func forget(c fiber.Ctx) error {
	return reply(c, db.Del(c.Params("key")))
}

func clone(c fiber.Ctx) error {
	return reply(c, db.Clone(c.Params("from_key"), c.Params("to_key")))
}

// reply maps a local write's result for the peer that sent it. 503 tells
// the peer to retry; a 4xx makes it drop the write.
func reply(c fiber.Ctx, err error) error {
	if err == nil {
		return c.Status(202).JSON(fiber.Map{"ok": true})
	}
	var je *db.JSONError
	switch {
	case errors.As(err, &je):
		return c.Status(422).JSON(fiber.Map{"error": je.Error()})
	case errors.Is(err, db.ErrWritesRefused):
		return c.Status(503).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(400).JSON(fiber.Map{"error": err.Error()})
}

func vacuum(c fiber.Ctx) error {
	db.Vacuum()

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

// Listen serves the peers port: the peers protocol, and the HTTP routes
// for peers from v0.7 or earlier, told apart by the first byte.
func Listen(addr string, peerAddrs []string) {
	Configure(peerAddrs)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Panic(err)
	}
	mux, app := newServer(ln)
	if err := app.Listener(mux, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		log.Panic(err)
	}
}

// newServer splits ln between the peers protocol, served at once, and the
// HTTP routes, which the caller serves from the returned listener.
func newServer(ln net.Listener) (*wire.Mux, *fiber.App) {
	mux := wire.NewMux(ln, func(first byte) bool { return first == hello[0] })
	mux.SetHandler(serveTCP)
	return mux, newApp()
}

// newApp builds the peer-facing routes, which apply writes to the local db only.
func newApp() *fiber.App {
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
	return app
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

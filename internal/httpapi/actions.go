package httpapi

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/shaunlee/simpleconf/internal/cluster"
	"github.com/shaunlee/simpleconf/internal/db"
)

func whole(c fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	return c.SendString(db.Get(""))
}

func single(c fiber.Ctx) error {
	c.Set("Content-Type", "application/json")
	return c.SendString(db.Get(c.Params("key")))
}

func update(c fiber.Ctx) error {
	return reply(c, cluster.ApplySetRaw(c.Params("key"), c.Body()))
}

func forget(c fiber.Ctx) error {
	return reply(c, cluster.ApplyDelete(c.Params("key")))
}

func clone(c fiber.Ctx) error {
	return reply(c, cluster.ApplyClone(c.Params("from_key"), c.Params("to_key")))
}

func vacuum(c fiber.Ctx) error {
	return reply(c, cluster.ApplyVacuum())
}

// reply maps a write result to its HTTP response.
func reply(c fiber.Ctx, err error) error {
	if err == nil {
		return c.Status(202).JSON(fiber.Map{"ok": true})
	}
	var je *db.JSONError
	if errors.As(err, &je) {
		return c.Status(422).JSON(fiber.Map{"error": je.Error()})
	}
	if nl, ok := cluster.AsNotLeader(err); ok {
		return c.Status(409).JSON(fiber.Map{"error": "not leader", "leader": nl.LeaderHTTPAddr})
	}
	return c.Status(400).JSON(fiber.Map{"error": err.Error()})
}

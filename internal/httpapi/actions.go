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
	k := c.Params("key")
	if err := cluster.ApplySetRaw(k, c.Body()); err != nil {
		var je *db.JSONError
		if errors.As(err, &je) {
			return c.Status(422).JSON(fiber.Map{"error": je.Error()})
		}
		if nl, ok := cluster.AsNotLeader(err); ok {
			return c.Status(409).JSON(fiber.Map{"error": "not leader", "leader": nl.LeaderHTTPAddr})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func forget(c fiber.Ctx) error {
	k := c.Params("key")

	if err := cluster.ApplyDelete(k); err != nil {
		if nl, ok := cluster.AsNotLeader(err); ok {
			return c.Status(409).JSON(fiber.Map{"error": "not leader", "leader": nl.LeaderHTTPAddr})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func clone(c fiber.Ctx) error {
	fk := c.Params("from_key")
	tk := c.Params("to_key")

	if err := cluster.ApplyClone(fk, tk); err != nil {
		if nl, ok := cluster.AsNotLeader(err); ok {
			return c.Status(409).JSON(fiber.Map{"error": "not leader", "leader": nl.LeaderHTTPAddr})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

func vacuum(c fiber.Ctx) error {
	if err := cluster.ApplyVacuum(); err != nil {
		if nl, ok := cluster.AsNotLeader(err); ok {
			return c.Status(409).JSON(fiber.Map{"error": "not leader", "leader": nl.LeaderHTTPAddr})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(202).JSON(fiber.Map{"ok": true})
}

package peers

import (
	"errors"
	"github.com/json-iterator/go"
	"github.com/kataras/iris"
	"github.com/parnurzeal/gorequest"
	"github.com/shaunlee/simpleconf/models"
	"log"
	"time"
)

var (
	json  = jsoniter.ConfigCompatibleWithStandardLibrary
	peers []string
)

func whole(ctx iris.Context) {
	ctx.ContentType("application/json")
	ctx.WriteString(models.Configuration)
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

func rewrite_aof(ctx iris.Context) {
	models.RewriteAof()

	ctx.JSON(iris.Map{
		"ok": true,
	})
}

func Restore(peers []string) error {
	if len(peers) == 0 {
		return nil
	}

	for _, addr := range peers {
		url := addr + "/db"
		log.Println("trying to restore from", url)
		if resp, body, err := (gorequest.New().Timeout(2 * time.Second)).Get(url).End(); err != nil {
			log.Println("failed to restore", err)
			continue
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Println("failed to restore", resp.Status, body)
				continue
			}

			models.Configuration = body
			models.RewriteAof()
			return nil
		}
	}

	return errors.New("There is no valid peer to be restore from")
}

func SyncUpdate(key string, value interface{}) error {
	for _, addr := range peers {
		url := addr + "/db/" + key
		v, _ := json.Marshal(value)
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Post(url).Send(string(v)).End(); err != nil {
			return err[0]
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return errors.New(resp.Status)
			}
		}
	}
	return nil
}

func SyncDelete(key string) error {
	for _, addr := range peers {
		url := addr + "/db/" + key
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Delete(url).End(); err != nil {
			return err[0]
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return errors.New(resp.Status)
			}
		}
	}
	return nil
}

func SyncClone(fromKey, toKey string) error {
	for _, addr := range peers {
		url := addr + "/clone/" + fromKey + "/" + toKey
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Post(url).End(); err != nil {
			return err[0]
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return errors.New(resp.Status)
			}
		}
	}
	return nil
}

func SyncRewriteAof() error {
	for _, addr := range peers {
		url := addr + "/rewriteaof"
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Post(url).End(); err != nil {
			return err[0]
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return errors.New(resp.Status)
			}
		}
	}
	return nil
}

func Listen(addr string, peerAddrs []string) {
	peers = peerAddrs

	app := iris.New()

	app.Get("/db", whole)
	app.Post("/db/{key}", update)
	app.Delete("/db/{key}", forget)
	app.Post("/clone/{from_key}/{to_key}", clone)
	app.Post("/rewriteaof", rewrite_aof)

	app.Run(iris.Addr(addr))
}

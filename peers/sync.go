package peers

import (
	"github.com/parnurzeal/gorequest"
	"github.com/shaunlee/simpleconf/models"
	"log"
	"time"
)

func Restore(peers []string) {
	if len(peers) == 0 {
		return
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
			break
		}
	}
}

func SyncUpdate(key string, value interface{}) {
	for _, addr := range peers {
		url := addr + "/db/" + key
		v, _ := json.Marshal(value)
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Post(url).Send(string(v)).End(); err != nil {
			log.Println("failed to sync", url, err)
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Println("failed to sync", url, resp.Status)
			}
		}
	}
}

func SyncDelete(key string) {
	for _, addr := range peers {
		url := addr + "/db/" + key
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Delete(url).End(); err != nil {
			log.Println("failed to sync", url, err)
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Println("failed to sync", url, resp.Status)
			}
		}
	}
}

func SyncClone(fromKey, toKey string) {
	for _, addr := range peers {
		url := addr + "/clone/" + fromKey + "/" + toKey
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Post(url).End(); err != nil {
			log.Println("failed to sync", url, err)
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Println("failed to sync", url, resp.Status)
			}
		}
	}
}

func SyncRewriteAof() {
	for _, addr := range peers {
		url := addr + "/rewriteaof"
		if resp, _, err := (gorequest.New().Timeout(2 * time.Second)).Post(url).End(); err != nil {
			log.Println("failed to sync", url, err)
		} else {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Println("failed to sync", url, resp.Status)
			}
		}
	}
}

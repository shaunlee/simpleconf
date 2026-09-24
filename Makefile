PKGS := $(shell go list ./...)

.PHONY: dev
dev:
	TCP_LISTEN=:23466 go run ./cmd/simpleconf

.PHONY: bench
bench:
	go run ./cmd/simpleconf-bench

.PHONY: build
build:
	CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -ldflags="-s -w" -a -v -o simpleconf ./cmd/simpleconf

.PHONY: test
test:
	go test $(PKGS)

.PHONY: cover
cover:
	go test -cover $(PKGS)

IMAGE   ?= shonhen/simpleconf
VERSION ?= $(shell git describe --tags --always --dirty)

.PHONY: docker
docker:
	docker build -f docker/Dockerfile -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: docker-push
docker-push: docker
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

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

IMAGE     ?= shonhen/simpleconf
VERSION   ?= $(shell git describe --tags --always --dirty)
PLATFORMS ?= linux/amd64,linux/arm64

# Builds for this machine's platform only, into the local image store.
.PHONY: docker
docker:
	docker build -f docker/Dockerfile -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

# A multi-platform image cannot be loaded locally, so it is built and pushed
# in one step.
.PHONY: docker-push
docker-push:
	docker buildx build -f docker/Dockerfile --platform $(PLATFORMS) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .

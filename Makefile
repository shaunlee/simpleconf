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

.PHONY: docker
docker:
	cd docker && docker build -t shonhen/simpleconf . && docker push shonhen/simpleconf

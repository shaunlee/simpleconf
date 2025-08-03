.PHONY: dev
dev:
	TCP_LISTEN=:23466 go run ./main.go

.PHONY: build
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -a -v -o simpleconf .

.PHONY: docker
docker:
	cd docker && docker build -t shonhen/simpleconf . && docker push shonhen/simpleconf

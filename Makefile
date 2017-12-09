build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -a -v -o simpleconf .

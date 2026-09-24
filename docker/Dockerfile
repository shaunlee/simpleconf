# Build from the repository root:
#   docker build -f docker/Dockerfile -t shonhen/simpleconf .
# make docker-push builds for linux/amd64 and linux/arm64 and pushes both.
# The build stage runs on the builder's own platform and cross-compiles, so a
# multi-platform build does not run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOEXPERIMENT=greenteagc \
    go build -ldflags="-s -w" -o /out/simpleconf ./cmd/simpleconf

FROM scratch
COPY --from=build /out/simpleconf /simpleconf
# The server reads configs/config.yml relative to its working directory, /.
COPY docker/config.yml /configs/config.yml
VOLUME /data
EXPOSE 23456
ENTRYPOINT ["/simpleconf"]

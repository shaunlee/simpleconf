# Build from the repository root:
#   docker build -f docker/Dockerfile -t shonhen/simpleconf .
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -ldflags="-s -w" -o /out/simpleconf ./cmd/simpleconf

FROM scratch
COPY --from=build /out/simpleconf /simpleconf
# The server reads configs/config.yml relative to its working directory, /.
COPY docker/config.yml /configs/config.yml
VOLUME /data
EXPOSE 23456
ENTRYPOINT ["/simpleconf"]

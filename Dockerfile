# Build the CLI from source. For released versions prefer the published image:
#   docker run --rm ghcr.io/inovector/mixpost-cli:latest version
FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/inovector/mixpost-cli/internal/cli.version=${VERSION}" \
    -o /out/mixpost ./cmd/mixpost

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/mixpost /usr/local/bin/mixpost
ENTRYPOINT ["mixpost"]

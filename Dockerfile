# syntax=docker/dockerfile:1
# Final image: scratch + static binary + CA bundle.
# No shell or package manager in the runtime image.

FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Release images: pass the Git tag, e.g. `docker buildx build --build-arg VERSION=v1.2.3 ...`
# (GitHub Actions sets this from github.ref_name.) Local builds omit it → default "dev".
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
    -ldflags="-s -w -X github.com/lanby-dev/lanby-relay/internal/relay.Version=${VERSION}" \
    -o /bin/relay ./cmd/relay

FROM alpine:3.20 AS ca-bundle
RUN apk add --no-cache ca-certificates

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.version="${VERSION}"

COPY --from=ca-bundle /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /bin/relay /bin/relay

ENTRYPOINT ["/bin/relay"]

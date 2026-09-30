# syntax=docker/dockerfile:1.7

# ---- build stage -----------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage ---------------------------------------------------------
# distroless/static ships ca-certificates and tzdata, runs as non-root and has
# no shell. Migrations are embedded in the binary (internal/crm/migrations).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server

ENV CALLGO_HTTP_ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]

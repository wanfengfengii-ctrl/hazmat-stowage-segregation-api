# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# build: compile the API binary with the Go 1.25 toolchain.
# ---------------------------------------------------------------------------
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---------------------------------------------------------------------------
# runtime: the only long-running service — the adjudication API.
# ---------------------------------------------------------------------------
FROM alpine:3.21 AS runtime
RUN adduser -D -u 10001 api
COPY --from=build /out/api /usr/local/bin/api
USER api
EXPOSE 8080
ENV PORT=8080
ENV GIN_MODE=release
ENTRYPOINT ["api"]

# ---------------------------------------------------------------------------
# verify: one-shot acceptance service. Runs the standard-library test suite
# against the live API pointed to by API_BASE_URL and exits with the result.
# ---------------------------------------------------------------------------
FROM golang:1.25-bookworm AS verify
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
ENTRYPOINT ["go", "test", "./...", "-count=1", "-v"]

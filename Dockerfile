# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
# NOTE: this image builds geodrill as a standalone module and therefore requires
# github.com/supercakecrumb/engram to be published (the local go.work workspace
# is not available inside the build context). During the parallel dev phase,
# run the bot locally with `go run ./cmd/bot` instead.
FROM golang:1.26 AS build
WORKDIR /src
ENV GOTOOLCHAIN=auto GOWORK=off CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot \
 && go build -trimpath -ldflags="-s -w" -o /out/ingest ./cmd/ingest

# ── runtime stage ────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/bot /app/bot
COPY --from=build /out/ingest /app/ingest
# migrations are embedded in the binary; seeds are read at ingest time
COPY --from=build /src/seeds /app/seeds
USER nonroot:nonroot
ENTRYPOINT ["/app/bot"]

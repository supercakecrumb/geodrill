# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /src
ENV GOTOOLCHAIN=auto CGO_ENABLED=0
COPY . .
RUN go mod download
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

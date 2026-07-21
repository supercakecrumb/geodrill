# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /src
ENV GOTOOLCHAIN=auto CGO_ENABLED=0
COPY . .
RUN go mod download
RUN go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot \
 && go build -trimpath -ldflags="-s -w" -o /out/ingest ./cmd/ingest
# Bake the Natural Earth basemap (~24 MB, public domain) into the image so the
# runtime bot can render city maps on demand (internal/citymap.Renderer) with no
# offline batch. The build stage has network (the golang image ships curl); the
# script downloads into data/naturalearth/, which we COPY into the runtime below
# at the NATURAL_EARTH_PATH default. (data/ is gitignored + dockerignored, so
# this file is always fetched fresh at build, never carried from the host.)
RUN bash scripts/fetch-naturalearth.sh

# ── runtime stage ────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/bot /app/bot
COPY --from=build /out/ingest /app/ingest
# migrations are embedded in the binary; seeds are read at ingest time
COPY --from=build /src/seeds /app/seeds
# Natural Earth basemap for on-demand city-map rendering (NATURAL_EARTH_PATH
# default, relative to WORKDIR /app).
COPY --from=build /src/data/naturalearth/ne_10m_admin_0_countries.geojson /app/data/naturalearth/ne_10m_admin_0_countries.geojson
USER nonroot:nonroot
ENTRYPOINT ["/app/bot"]

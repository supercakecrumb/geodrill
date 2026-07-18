# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
# TEMPORARY (until engram v0.3.0 is tagged): github.com/supercakecrumb/engram
# is not yet published, so geodrill resolves it locally via the committed
# go.work (../../Packages/engram) rather than a versioned `require` in
# go.mod. A Docker build context can't see that relative workspace path, so
# release.yml checks out supercakecrumb/engram into an `engram/`
# subdirectory of THIS build context (see .github/workflows/release.yml),
# and we splice it in below with `go mod edit -replace` + a matching
# `-require` (a `replace` alone doesn't satisfy the import unless go.mod also
# has *a* require line for it — the version there is a placeholder, ignored
# because the replace target is a local directory, not a proxy-fetched
# module). Once engram v0.3.0 is tagged, geodrill's go.mod pins a real
# `require github.com/supercakecrumb/engram vX.Y.Z`, and go.work is dropped
# (final wave, per vibe/geodrill-architecture.md §7.3): delete the
# `COPY engram/` line and both `go mod edit` lines below, and go back to a
# plain `go mod download` before `COPY . .`.
#
# During the parallel dev phase, run the bot locally with `go run ./cmd/bot`
# instead — there's no `engram/` directory to splice in outside CI.
FROM golang:1.26 AS build
WORKDIR /src
ENV GOTOOLCHAIN=auto GOWORK=off CGO_ENABLED=0
COPY . .
COPY engram/ /engram/
RUN go mod edit -require=github.com/supercakecrumb/engram@v0.0.0-00010101000000-000000000000 \
 && go mod edit -replace=github.com/supercakecrumb/engram=/engram
# -mod=mod (build-container-local only, not `go mod tidy`): geodrill's
# committed go.sum never needed an entry for engram's own dependency
# (go-fsrs) because locally engram is resolved via go.work, which tracks
# checksums separately in the (gitignored) go.work.sum. -mod=mod lets `go
# build` add just that missing entry as it resolves the graph, instead of
# failing with "missing go.sum entry". This never touches the real repo's
# go.sum on disk — only this ephemeral build stage's copy.
RUN GOFLAGS=-mod=mod go mod download
RUN GOFLAGS=-mod=mod go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot \
 && GOFLAGS=-mod=mod go build -trimpath -ldflags="-s -w" -o /out/ingest ./cmd/ingest

# ── runtime stage ────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/bot /app/bot
COPY --from=build /out/ingest /app/ingest
# migrations are embedded in the binary; seeds are read at ingest time
COPY --from=build /src/seeds /app/seeds
USER nonroot:nonroot
ENTRYPOINT ["/app/bot"]

// Package migrations embeds the golang-migrate SQL files so the bot can apply
// them at startup (idempotent up). The same .sql files are also consumed
// directly by the golang-migrate CLI / the docker `migrate` service.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS

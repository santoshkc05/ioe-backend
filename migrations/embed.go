// Package migrations embeds the goose SQL migrations for every schema.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

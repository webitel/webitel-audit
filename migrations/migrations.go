// Package migrations embeds the goose SQL migrations of the audit schema.
package migrations

import "embed"

const VersionTable = "public.webitel_audit_schema_version"

//go:embed *.sql
var EmbedMigrations embed.FS

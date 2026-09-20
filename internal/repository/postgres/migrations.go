package postgres

import _ "embed"

//go:embed migrations/001_init.sql
var initialMigration string

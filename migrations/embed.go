// Package migrations owns the SQL files embedded in the Mithra binary.
package migrations

import "embed"

// Files contains every numbered SQL migration compiled into the application.
//
//go:embed *.sql
var Files embed.FS

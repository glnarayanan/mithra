// Package web owns Mithra's first-party browser assets.
package web

import "embed"

// Files contains templates and static assets compiled into the Mithra binary.
//
//go:embed templates/*.html templates/auth/*.html templates/settings/*.html static/*
var Files embed.FS

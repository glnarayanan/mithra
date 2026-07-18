// Package web owns Mithra's first-party browser assets.
package web

import "embed"

// Files contains templates and static assets compiled into the Mithra binary.
//
//go:embed templates/auth/*.html templates/brief/*.html templates/capture/*.html templates/finance/*.html templates/health/*.html templates/imports/*.html templates/planning/*.html templates/review/*.html templates/settings/*.html static/*
var Files embed.FS

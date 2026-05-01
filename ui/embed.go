// Package ui exposes the embedded web/static filesystem so the HTTP
// server can serve it without depending on the on-disk layout. Mirrors
// clonarr/ui/embed.go.
package ui

import "embed"

//go:embed static
var StaticFiles embed.FS

// Package assets embeds the browser-facing templates and static files so the
// server can ship as a single binary.
package assets

import "embed"

// FS contains web/templates and web/static. Keep the embed directive in this
// package: //go:embed cannot reference parent directories.
//
//go:embed templates/*.html static
var FS embed.FS

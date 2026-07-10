// Package web embeds the server-rendered templates and static assets
// so the nixbox binary is fully self-contained (no CDN, no files on
// disk).
package web

import "embed"

//go:embed templates static
var FS embed.FS

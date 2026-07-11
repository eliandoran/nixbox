// Package web embeds the server-rendered templates and static assets
// so the nixbox binary is fully self-contained (no CDN, no files on
// disk).
//
// static/app.js is not checked in: esbuild bundles it from the
// TypeScript in src/ (`just bundle`, `just dev`, or `go generate
// ./web`; the nix package bundles in preBuild). The dedicated
// static/app.js pattern below turns a missing bundle into a compile
// error instead of the directory pattern's silent 404.
package web

import "embed"

//go:generate esbuild src/main.ts --bundle --format=iife --outfile=static/app.js

//go:embed templates static i18n
//go:embed static/app.js
var FS embed.FS

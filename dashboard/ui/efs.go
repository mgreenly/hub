// Package ui owns the dashboard's embedded front-end assets: HTML templates
// under html/ and static files (css, js) under static/. It exposes them as a
// single embed.FS so the server package depends on the bundle, not on where the
// bytes live on disk.
package ui

import "embed"

//go:embed html static
var Files embed.FS

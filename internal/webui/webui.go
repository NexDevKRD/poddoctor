// Package webui embeds the built dashboard SPA (see web/) so both
// cmd/main.go (per-cluster dashboard) and cmd/hub (fleet hub) can serve
// the same frontend without shipping it separately. The SPA build writes
// straight into ./dist (see web/vite.config.ts's outDir) — run
// `npm run build` in web/ before `go build` picks this package up.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var distFS embed.FS

var static, _ = fs.Sub(distFS, "dist")

// Handler serves the built dashboard SPA's static assets, including
// index.html at "/".
func Handler() http.Handler {
	return http.FileServer(http.FS(static))
}

// Package ui embeds the built admin console (web/admin). `make web` builds it
// into dist/app; without a build the console serves a placeholder page.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const placeholder = `<!doctype html><meta charset="utf-8"><title>sql-mcp-server 控制台</title>
<p>控制台尚未构建：运行 <code>make web</code> 后重新编译服务。</p>`

// contentSecurityPolicy allows only same-origin resources. Inline styles are
// needed because the component library injects its styles at runtime.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Handler serves the single-page app mounted at prefix ("/admin/"): files
// that exist are served directly, every other path gets index.html so the
// client-side router can resolve it.
func Handler(prefix string) http.Handler {
	app, err := fs.Sub(dist, "dist/app")
	if err != nil {
		panic(err)
	}
	index, indexErr := fs.ReadFile(app, "index.html")
	files := http.FileServerFS(app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if indexErr != nil {
			h.Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholder))
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), strings.TrimSuffix(prefix, "/"))
		name = strings.TrimPrefix(name, "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(app, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + name
				files.ServeHTTP(w, r2)
				return
			}
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist
var studio embed.FS

// Handler serves the Studio build embedded in this module.
func Handler() http.Handler {
	root, err := fs.Sub(studio, "dist")
	if err != nil {
		panic(err)
	}
	return HandlerFor(root)
}

// HandlerFor serves a single-page app build: files as they are, index.html
// for every other path.
func HandlerFor(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested != "." && requested != "" {
			if file, err := root.Open(requested); err == nil {
				_ = file.Close()
				if strings.Contains(path.Base(requested), ".") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			// Missing compiled assets must fail as assets. Returning index.html
			// here gives dynamic imports a misleading 200 text/html response and
			// can leave an updated desktop tab showing its previous route.
			if strings.HasPrefix(requested, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}

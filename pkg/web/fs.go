package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var staticFS embed.FS

// Handler returns an http.Handler that serves the embedded static files.
// It includes SPA (Single Page Application) logic to serve index.html for 404s.
func Handler() http.Handler {
	// Root is "static" because files are in pkg/web/static
	fsys, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}

	fileServer := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		
		// 1. Try to serve exact file
		// Clean path to prevent directory traversal is handled by http.FS, 
		// but checking existence helps avoid 404 handler loop if we were strict.
		// However, for embedded FS, we can just Open.
		f, err := fsys.Open(strings.TrimPrefix(path, "/"))
		if err == nil {
			stat, _ := f.Stat()
			f.Close()
			// If it's a file, serve it
			if !stat.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
			// If directory, checking for index.html is standard, but in SPA build assets usually flat or strict.
		}

		// 2. Fallback to index.html for SPA routing (except for /api/ which should be handled upstream)
		// We assumes /api/ is handled before this handler.
		// Serve index.html
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

package main

import (
	"embed"
	"log"
	"net/http"
)

// Embed the UI so the executor remains a single deployable binary.
//
//go:embed web/index.html web/style.css web/app.mjs
var notebookFiles embed.FS

func notebookAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := notebookFiles.ReadFile("web/" + name)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if _, err := w.Write(data); err != nil {
			log.Printf("ui: write response: %v", err)
		}
	}
}

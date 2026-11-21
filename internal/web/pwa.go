package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"text/template"
)

//go:embed pwa/sw.js
var pwaFS embed.FS

var precacheStatic = []string{"style.css", "app.js", "icons/icon-192.png", "icons/favicon-32.png"}

func hashFS(fsys fs.FS) (string, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(p))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func (s *Server) asset(name string) string {
	return "/static/" + name + "?v=" + s.version
}

func (s *Server) buildServiceWorker() error {
	tmpl, err := template.ParseFS(pwaFS, "pwa/sw.js")
	if err != nil {
		return err
	}
	precache := []string{"/", "/offline"}
	for _, p := range precacheStatic {
		precache = append(precache, s.asset(p))
	}
	pj, err := json.Marshal(precache)
	if err != nil {
		return err
	}
	vj, _ := json.Marshal(s.version)
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{"Version": string(vj), "Precache": string(pj)}); err != nil {
		return err
	}
	s.serviceWorker = buf.Bytes()
	return nil
}

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(s.serviceWorker)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(staticFS, "static/manifest.webmanifest")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "offline.html", struct{ Title string }{"Offline"})
}

func staticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if !strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

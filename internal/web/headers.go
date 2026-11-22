package web

import (
	"net/http"
	"strings"
)

var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' https://unpkg.com",
	"style-src 'self' https://unpkg.com 'unsafe-inline'",
	"style-src-elem 'self' https://unpkg.com",
	"img-src 'self' data: blob: https://tiles.versatiles.org",
	"connect-src 'self' https://unpkg.com https://tiles.versatiles.org",
	"worker-src 'self' blob:",
	"child-src blob:",
	"font-src 'self'",
	"manifest-src 'self'",
	"object-src 'none'",
	"base-uri 'none'",
	"form-action 'self'",
	"frame-ancestors 'none'",
}, "; ")

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(self), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil || (fromProxy(r) && r.Header.Get("X-Forwarded-Proto") == "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

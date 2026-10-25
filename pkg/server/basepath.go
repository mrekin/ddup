package server

import "net/http"

// withBasePath serves the given handler under a base path
func withBasePath(basePath string, next http.Handler) http.Handler {
	strip := http.StripPrefix(basePath, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect requests without the trailing slash (e.g. "/ddup" to "/ddup/")
		if r.URL.Path == basePath {
			http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
			return
		}
		strip.ServeHTTP(w, r)
	})
}

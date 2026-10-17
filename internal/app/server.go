package app

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Addr            string
	DataDir         string
	Hostnames       []string
	TLSCert         string
	TLSKey          string
	TrustCloudflare bool
	AdminUser       string
	AdminPassword   string
}

func ConfigFromEnv() Config {
	cfg := Config{
		Addr:            envOr("SP_ADDR", ":8443"),
		DataDir:         envOr("SP_DATA", "./data"),
		Hostnames:       splitList(envOr("SP_HOSTNAMES", "localhost,127.0.0.1,::1")),
		TLSCert:         os.Getenv("SP_TLS_CERT"),
		TLSKey:          os.Getenv("SP_TLS_KEY"),
		TrustCloudflare: os.Getenv("SP_TRUST_CLOUDFLARE") == "1",
		AdminUser:       envOr("SP_ADMIN_USER", "admin"),
		AdminPassword:   os.Getenv("SP_ADMIN_PASSWORD"),
	}
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

type Server struct {
	cfg     Config
	store   *Store
	hub     *hub
	limiter *failureLimiter
	web     fs.FS
	skill   []byte
}

func OpenDataStore(cfg Config) (*Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	return OpenStore(filepath.Join(cfg.DataDir, "scratchpads.db"))
}

func NewServer(cfg Config, store *Store, web fs.FS, skill []byte) *Server {
	return &Server{cfg: cfg, store: store, hub: newHub(), limiter: newFailureLimiter(), web: web, skill: skill}
}

func (srv *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.Handler { return srv.authenticate(h) }

	mux.Handle("GET /static/", http.StripPrefix("/static/", noDirListing(http.FileServerFS(srv.web))))
	mux.HandleFunc("GET /{$}", srv.serveIndex)
	mux.HandleFunc("GET /skill.md", srv.serveSkill)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /api/session", srv.login)
	mux.HandleFunc("DELETE /api/session", srv.logout)

	mux.Handle("GET /api/me", auth(srv.me))

	mux.Handle("GET /api/users", auth(srv.listUsers))
	mux.Handle("POST /api/users", auth(srv.createUser))
	mux.Handle("DELETE /api/users/{name}", auth(srv.deleteUser))

	mux.Handle("GET /api/agents", auth(srv.listAgents))
	mux.Handle("POST /api/agents", auth(srv.createAgent))
	mux.Handle("DELETE /api/agents/{slot}", auth(srv.deleteAgent))

	mux.Handle("GET /api/tokens", auth(srv.listTokens))
	mux.Handle("POST /api/tokens", auth(srv.createToken))
	mux.Handle("DELETE /api/tokens/{id}", auth(srv.deleteToken))

	mux.Handle("GET /api/topics", auth(srv.listTopics))
	mux.Handle("POST /api/topics", auth(srv.createTopic))
	mux.Handle("DELETE /api/topics/{topic}", auth(srv.deleteTopic))
	mux.Handle("GET /api/topics/{topic}/grants", auth(srv.listGrants))
	mux.Handle("PUT /api/topics/{topic}/grants/{subject...}", auth(srv.setGrant))
	mux.Handle("DELETE /api/topics/{topic}/grants/{subject...}", auth(srv.deleteGrant))

	mux.Handle("GET /api/t/{topic}/tree", auth(srv.tree))
	mux.Handle("GET /api/t/{topic}/search", auth(srv.search))
	mux.Handle("POST /api/t/{topic}/move", auth(srv.move))
	mux.Handle("GET /api/t/{topic}/f/{path...}", auth(srv.readFile))
	mux.Handle("PUT /api/t/{topic}/f/{path...}", auth(srv.writeFile))
	mux.Handle("POST /api/t/{topic}/f/{path...}", auth(srv.appendFile))
	mux.Handle("DELETE /api/t/{topic}/f/{path...}", auth(srv.deleteFile))
	mux.Handle("GET /api/t/{topic}/history/{path...}", auth(srv.history))
	mux.Handle("GET /api/t/{topic}/c/{path...}", auth(srv.listComments))
	mux.Handle("POST /api/t/{topic}/c/{path...}", auth(srv.addComment))
	mux.Handle("POST /api/comments/{id}/resolve", auth(srv.resolveComment(true)))
	mux.Handle("POST /api/comments/{id}/reopen", auth(srv.resolveComment(false)))
	mux.Handle("DELETE /api/comments/{id}", auth(srv.deleteComment))

	mux.Handle("GET /api/inbox", auth(srv.inbox))
	mux.Handle("POST /api/inbox/read", auth(srv.markRead))

	return srv.securityHeaders(srv.logRequests(mux))
}

func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (srv *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (srv *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %s %d %s", srv.clientIP(r), r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// ListenAndServe serves HTTPS only; there is no plaintext listener.
func (srv *Server) ListenAndServe(cert tls.Certificate) error {
	s := &http.Server{
		Addr:              srv.cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		},
		ErrorLog: log.New(tlsNoiseFilter{}, "", log.LstdFlags),
	}
	return s.ListenAndServeTLS("", "")
}

// tlsNoiseFilter drops handshake errors from clients that reject the
// self-signed certificate; they are expected and not actionable.
type tlsNoiseFilter struct{}

func (tlsNoiseFilter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "TLS handshake error") {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (srv *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	raw, err := fs.ReadFile(srv.web, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(raw)
}

func (srv *Server) serveSkill(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write(srv.skill)
}

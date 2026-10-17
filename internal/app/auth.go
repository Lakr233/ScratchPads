package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	tokenPrefix   = "sp_"
	sessionCookie = "sp_session"
	csrfHeader    = "X-SP-CSRF"
)

type ctxKey struct{}

func principalFrom(r *http.Request) *Principal {
	p, _ := r.Context().Value(ctxKey{}).(*Principal)
	return p
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// IssueToken mints a new random token for the principal. Only its hash is
// stored, so the returned plaintext is shown exactly once.
func (s *Store) IssueToken(p *Principal, label string) (string, int64, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	id, err := s.InsertToken(hashToken(token), p.ID, label)
	return token, id, err
}

// failureLimiter throttles clients that keep presenting bad tokens.
type failureLimiter struct {
	mu      sync.Mutex
	entries map[string]*failureEntry
}

type failureEntry struct {
	count int
	reset time.Time
}

const (
	maxFailures   = 20
	failureWindow = time.Minute
)

func newFailureLimiter() *failureLimiter {
	return &failureLimiter{entries: map[string]*failureEntry{}}
}

func (l *failureLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	return e != nil && time.Now().Before(e.reset) && e.count >= maxFailures
}

func (l *failureLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if e == nil || time.Now().After(e.reset) {
		e = &failureEntry{reset: time.Now().Add(failureWindow)}
		l.entries[ip] = e
	}
	e.count++
	if len(l.entries) > 10000 {
		for k, v := range l.entries {
			if time.Now().After(v.reset) {
				delete(l.entries, k)
			}
		}
	}
}

func (srv *Server) clientIP(r *http.Request) string {
	if srv.cfg.TrustCloudflare {
		if ip := r.Header.Get("CF-Connecting-IP"); net.ParseIP(ip) != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestToken returns the bearer token or session cookie of a request.
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		_, value, _ := strings.Cut(h, " ")
		return strings.TrimSpace(value)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// authenticate resolves the caller from a bearer token, or from the session
// cookie set by the web UI. Cookie-authenticated writes must carry the CSRF
// header, which a cross-site form or image can never send.
func (srv *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := srv.clientIP(r)
		token, fromCookie := "", false
		if h := r.Header.Get("Authorization"); h != "" {
			scheme, value, _ := strings.Cut(h, " ")
			if !strings.EqualFold(scheme, "Bearer") {
				writeError(w, http.StatusUnauthorized, "use Authorization: Bearer <token>")
				return
			}
			token = strings.TrimSpace(value)
		} else if c, err := r.Cookie(sessionCookie); err == nil {
			token, fromCookie = c.Value, true
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}
		p, err := srv.store.PrincipalByTokenHash(hashToken(token))
		if errors.Is(err, ErrNotFound) {
			// Throttle only failures, so a flood of bad tokens from one
			// address (e.g. the shared tunnel IP) never locks out valid ones.
			if srv.limiter.blocked(ip) {
				writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
				return
			}
			srv.limiter.fail(ip)
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "auth lookup failed")
			return
		}
		if fromCookie && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(csrfHeader) != "1" {
			writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

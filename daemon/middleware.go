package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxRateLimiterKeys = 4096

type rateLimitEntry struct {
	windowStart time.Time
	count       int
}

type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]rateLimitEntry
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		entries: make(map[string]rateLimitEntry),
	}
}

func (rl *RateLimiter) Allow(key string, now time.Time) bool {
	if rl.limit <= 0 || rl.window <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	if entry, ok := rl.entries[key]; ok {
		if now.Sub(entry.windowStart) >= rl.window {
			rl.entries[key] = rateLimitEntry{windowStart: now, count: 1}
			return true
		}
		if entry.count >= rl.limit {
			return false
		}
		entry.count++
		rl.entries[key] = entry
		return true
	}

	if len(rl.entries) >= maxRateLimiterKeys {
		rl.evictLocked(now)
	}

	rl.entries[key] = rateLimitEntry{windowStart: now, count: 1}
	return true
}

func (rl *RateLimiter) evictLocked(now time.Time) {
	for k, v := range rl.entries {
		if now.Sub(v.windowStart) >= rl.window {
			delete(rl.entries, k)
		}
	}
	if len(rl.entries) < maxRateLimiterKeys {
		return
	}

	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, v := range rl.entries {
		if first || v.windowStart.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.windowStart
			first = false
		}
	}
	if !first {
		delete(rl.entries, oldestKey)
	}
}

func isValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if !isValidRequestID(reqID) {
			reqID = generateRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r)
	})
}

func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type HTTPMetrics struct {
	RequestsTotal       atomic.Uint64
	ErrorsTotal         atomic.Uint64
	BudgetExceededTotal atomic.Uint64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.RequestsTotal.Add(1)
		if rec.status >= http.StatusBadRequest {
			m.ErrorsTotal.Add(1)
		}
		if rec.status == http.StatusPaymentRequired {
			m.BudgetExceededTotal.Add(1)
		}
	})
}

func ParseTrustedProxies(raw string) []net.IPNet {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var nets []net.IPNet
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, ipNet, err := net.ParseCIDR(entry); err == nil && ipNet != nil {
				nets = append(nets, *ipNet)
			}
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				nets = append(nets, net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)})
			} else {
				nets = append(nets, net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
			}
		}
	}
	return nets
}

func isTrustedProxy(ip net.IP, trusted []net.IPNet) bool {
	if ip == nil {
		return false
	}
	for i := range trusted {
		if trusted[i].Contains(ip) {
			return true
		}
	}
	return false
}

func ClientIP(r *http.Request, trusted []net.IPNet) string {
	remoteHost := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remoteHost = host
	}
	remoteHost = strings.TrimSpace(remoteHost)
	remoteIP := net.ParseIP(strings.Trim(remoteHost, "[]"))
	if remoteIP == nil {
		return remoteHost
	}

	if remoteIP.IsLoopback() || isTrustedProxy(remoteIP, trusted) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			var firstValid net.IP
			for _, part := range parts {
				if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
					firstValid = ip
					break
				}
			}
			for i := len(parts) - 1; i >= 0; i-- {
				ip := net.ParseIP(strings.TrimSpace(parts[i]))
				if ip != nil && !ip.IsLoopback() && !isTrustedProxy(ip, trusted) {
					return ip.String()
				}
			}
			if firstValid != nil {
				return firstValid.String()
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			if ip := net.ParseIP(xri); ip != nil {
				return ip.String()
			}
		}
	}

	return remoteIP.String()
}

func RateLimitMiddleware(limiter *RateLimiter, nowFn func() time.Time) func(http.Handler) http.Handler {
	return RateLimitMiddlewareWithProxies(limiter, nil, nowFn)
}

func RateLimitMiddlewareWithProxies(limiter *RateLimiter, trusted []net.IPNet, nowFn func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if limiter == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}

			key := ClientIP(r, trusted)

			now := time.Now()
			if nowFn != nil {
				now = nowFn()
			}

			if !limiter.Allow(key, now) {
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error": "rate limit exceeded"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

package daemon

import (
	"log/slog"
	"net/http"
	"time"
)

type accessLogResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *accessLogResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *accessLogResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *accessLogResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func StructuredAccessLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if logger == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &accessLogResponseWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(rec, r)

			reqID := rec.Header().Get("X-Request-ID")
			if reqID == "" {
				reqID = r.Header.Get("X-Request-ID")
			}

			logger.Info(
				"http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.bytes),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("request_id", reqID),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

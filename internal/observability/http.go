package observability

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/auth"
	"go.opentelemetry.io/otel/trace"
)

const requestIDHeader = "X-Request-ID"

// RequestLogger emits one structured record after every request. It deliberately
// records the URL path without its query string so credentials or cursors cannot
// accidentally enter logs.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			started := time.Now()
			requestContext, capturedPrincipal := auth.CapturePrincipal(request.Context())
			request = request.WithContext(requestContext)
			requestID := normalizedRequestID(request.Header.Get(requestIDHeader))
			w.Header().Set(requestIDHeader, requestID)
			writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(writer, request)

			attributes := []any{
				"request_id", requestID,
				"method", request.Method,
				"path", request.URL.Path,
				"status", writer.status,
				"response_bytes", writer.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
			}
			principal, ok := auth.PrincipalFromContext(request.Context())
			if !ok {
				principal, ok = capturedPrincipal()
			}
			if ok {
				attributes = append(attributes, "actor", principal.Actor)
			}
			spanContext := trace.SpanContextFromContext(request.Context())
			if spanContext.IsValid() {
				attributes = append(attributes,
					"trace_id", spanContext.TraceID().String(),
					"span_id", spanContext.SpanID().String(),
				)
			}
			logger.InfoContext(request.Context(), "http request", attributes...)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.wrote {
		return
	}
	writer.status = status
	writer.wrote = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(contents []byte) (int, error) {
	if !writer.wrote {
		writer.WriteHeader(http.StatusOK)
	}
	written, err := writer.ResponseWriter.Write(contents)
	writer.bytes += written
	return written, err
}

// Unwrap allows http.ResponseController to retain Flush, Hijack, and deadline
// support through this middleware, including for server-sent event streams.
func (writer *statusWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func normalizedRequestID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 0 && len(value) <= 128 && validRequestID(value) {
		return value
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return "unavailable"
}

func validRequestID(value string) bool {
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

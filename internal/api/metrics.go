package api

import (
	"net/http"
	"sync/atomic"
)

type Metrics struct {
	requestsTotal uint64
	errorsTotal   uint64
	inFlight      int64
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		RequestsTotal: atomic.LoadUint64(&m.requestsTotal),
		ErrorsTotal:   atomic.LoadUint64(&m.errorsTotal),
		InFlight:      atomic.LoadInt64(&m.inFlight),
	}
}

type MetricsSnapshot struct {
	RequestsTotal uint64
	ErrorsTotal   uint64
	InFlight      int64
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&m.inFlight, 1)
		defer atomic.AddInt64(&m.inFlight, -1)

		atomic.AddUint64(&m.requestsTotal, 1)

		wrapped := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		if wrapped.status >= 400 {
			atomic.AddUint64(&m.errorsTotal, 1)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

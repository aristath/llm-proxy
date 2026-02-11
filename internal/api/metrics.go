package api

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	requestsTotal uint64
	errorsTotal   uint64
	inFlight      int64

	status2xx uint64
	status3xx uint64
	status4xx uint64
	status5xx uint64

	modelsTotal          uint64
	chatCompletionsTotal uint64
	responsesTotal       uint64
	otherTotal           uint64

	bytesSent uint64

	latencyTotalNs uint64
	latencyMaxNs   uint64

	modelMu     sync.RWMutex
	modelCounts map[string]*modelCounters
}

func NewMetrics() *Metrics {
	return &Metrics{
		modelCounts: make(map[string]*modelCounters),
	}
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	reqs := atomic.LoadUint64(&m.requestsTotal)
	latencyTotalNs := atomic.LoadUint64(&m.latencyTotalNs)
	latencyMaxNs := atomic.LoadUint64(&m.latencyMaxNs)
	avgLatencyMs := 0.0
	if reqs > 0 {
		avgLatencyMs = float64(latencyTotalNs) / float64(reqs) / float64(time.Millisecond)
	}
	snapshot := MetricsSnapshot{
		RequestsTotal: atomic.LoadUint64(&m.requestsTotal),
		ErrorsTotal:   atomic.LoadUint64(&m.errorsTotal),
		InFlight:      atomic.LoadInt64(&m.inFlight),
		Status2xx:     atomic.LoadUint64(&m.status2xx),
		Status3xx:     atomic.LoadUint64(&m.status3xx),
		Status4xx:     atomic.LoadUint64(&m.status4xx),
		Status5xx:     atomic.LoadUint64(&m.status5xx),

		ModelsTotal:          atomic.LoadUint64(&m.modelsTotal),
		ChatCompletionsTotal: atomic.LoadUint64(&m.chatCompletionsTotal),
		ResponsesTotal:       atomic.LoadUint64(&m.responsesTotal),
		OtherTotal:           atomic.LoadUint64(&m.otherTotal),

		BytesSent:    atomic.LoadUint64(&m.bytesSent),
		AvgLatencyMs: avgLatencyMs,
		MaxLatencyMs: float64(latencyMaxNs) / float64(time.Millisecond),
	}
	m.modelMu.RLock()
	snapshot.Models = make([]ModelStats, 0, len(m.modelCounts))
	for model, c := range m.modelCounts {
		avgLatencyMs := 0.0
		avgTokensPerCall := 0.0
		if c.RequestsTotal > 0 {
			avgLatencyMs = float64(c.LatencyTotalNs) / float64(c.RequestsTotal) / float64(time.Millisecond)
			avgTokensPerCall = float64(c.TokensTotal) / float64(c.RequestsTotal)
		}
		snapshot.Models = append(snapshot.Models, ModelStats{
			Model:            model,
			RequestsTotal:    c.RequestsTotal,
			ErrorsTotal:      c.ErrorsTotal,
			ChatCompletions:  c.ChatCompletions,
			Responses:        c.Responses,
			OtherRequests:    c.OtherRequests,
			TokensTotal:      c.TokensTotal,
			AvgLatencyMs:     avgLatencyMs,
			AvgTokensPerCall: avgTokensPerCall,
		})
	}
	m.modelMu.RUnlock()
	sort.Slice(snapshot.Models, func(i, j int) bool {
		if snapshot.Models[i].RequestsTotal == snapshot.Models[j].RequestsTotal {
			return snapshot.Models[i].Model < snapshot.Models[j].Model
		}
		return snapshot.Models[i].RequestsTotal > snapshot.Models[j].RequestsTotal
	})
	return snapshot
}

type MetricsSnapshot struct {
	RequestsTotal uint64
	ErrorsTotal   uint64
	InFlight      int64

	Status2xx uint64
	Status3xx uint64
	Status4xx uint64
	Status5xx uint64

	ModelsTotal          uint64
	ChatCompletionsTotal uint64
	ResponsesTotal       uint64
	OtherTotal           uint64

	BytesSent    uint64
	AvgLatencyMs float64
	MaxLatencyMs float64

	Models []ModelStats
}

type ModelStats struct {
	Model            string
	RequestsTotal    uint64
	ErrorsTotal      uint64
	ChatCompletions  uint64
	Responses        uint64
	OtherRequests    uint64
	TokensTotal      uint64
	AvgLatencyMs     float64
	AvgTokensPerCall float64
}

type modelCounters struct {
	RequestsTotal   uint64
	ErrorsTotal     uint64
	ChatCompletions uint64
	Responses       uint64
	OtherRequests   uint64
	TokensTotal     uint64
	LatencyTotalNs  uint64
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		atomic.AddInt64(&m.inFlight, 1)
		defer atomic.AddInt64(&m.inFlight, -1)

		atomic.AddUint64(&m.requestsTotal, 1)
		switch r.URL.Path {
		case "/v1/models":
			atomic.AddUint64(&m.modelsTotal, 1)
		case "/v1/chat/completions":
			atomic.AddUint64(&m.chatCompletionsTotal, 1)
		case "/v1/responses":
			atomic.AddUint64(&m.responsesTotal, 1)
		default:
			atomic.AddUint64(&m.otherTotal, 1)
		}

		wrapped := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		status := wrapped.statusCode()
		if status >= 400 {
			atomic.AddUint64(&m.errorsTotal, 1)
		}
		switch {
		case status >= 500:
			atomic.AddUint64(&m.status5xx, 1)
		case status >= 400:
			atomic.AddUint64(&m.status4xx, 1)
		case status >= 300:
			atomic.AddUint64(&m.status3xx, 1)
		default:
			atomic.AddUint64(&m.status2xx, 1)
		}
		atomic.AddUint64(&m.bytesSent, wrapped.bytesWritten)
		latencyNs := uint64(time.Since(startedAt))
		m.observeModel(
			wrapped.observedModel,
			r.URL.Path,
			status,
			latencyNs,
			wrapped.promptTokens,
			wrapped.completionTokens,
		)

		atomic.AddUint64(&m.latencyTotalNs, latencyNs)
		for {
			cur := atomic.LoadUint64(&m.latencyMaxNs)
			if latencyNs <= cur || atomic.CompareAndSwapUint64(&m.latencyMaxNs, cur, latencyNs) {
				break
			}
		}
	})
}

func (m *Metrics) observeModel(model string, path string, status int, latencyNs uint64, promptTokens uint64, completionTokens uint64) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	c := m.modelCounts[model]
	if c == nil {
		c = &modelCounters{}
		m.modelCounts[model] = c
	}
	c.RequestsTotal++
	if status >= 400 {
		c.ErrorsTotal++
	}
	switch path {
	case "/v1/chat/completions":
		c.ChatCompletions++
	case "/v1/responses":
		c.Responses++
	default:
		c.OtherRequests++
	}
	c.LatencyTotalNs += latencyNs
	c.TokensTotal += promptTokens + completionTokens
}

type statusRecorder struct {
	http.ResponseWriter
	status           int
	bytesWritten     uint64
	observedModel    string
	promptTokens     uint64
	completionTokens uint64
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	if n > 0 {
		r.bytesWritten += uint64(n)
	}
	return n, err
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *statusRecorder) SetObservedModel(model string) {
	r.observedModel = model
}

func (r *statusRecorder) AddObservedTokens(promptTokens uint64, completionTokens uint64) {
	r.promptTokens += promptTokens
	r.completionTokens += completionTokens
}

type modelObserver interface {
	SetObservedModel(string)
}

func ObserveModel(w http.ResponseWriter, model string) {
	if mw, ok := w.(modelObserver); ok {
		mw.SetObservedModel(model)
	}
}

type tokenObserver interface {
	AddObservedTokens(uint64, uint64)
}

func ObserveTokenUsage(w http.ResponseWriter, promptTokens uint64, completionTokens uint64) {
	if mw, ok := w.(tokenObserver); ok {
		mw.AddObservedTokens(promptTokens, completionTokens)
	}
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

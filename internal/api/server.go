package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llm-proxy/internal/openapiv1"
	"llm-proxy/internal/proxy"
)

type Server struct {
	router *proxy.Router
}

func NewServer(router *proxy.Router) *Server {
	return &Server{router: router}
}

func (s *Server) ListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.router.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	out := make([]openapiv1.Model, 0, len(models))
	for _, m := range models {
		owner := string(m.Backend)
		out = append(out, openapiv1.Model{
			Id:      m.ID,
			Object:  openapiv1.ModelObjectModel,
			OwnedBy: &owner,
		})
	}

	writeJSON(w, http.StatusOK, openapiv1.ModelListResponse{
		Object: openapiv1.List,
		Data:   out,
	})
}

func (s *Server) CreateChatCompletion(w http.ResponseWriter, r *http.Request) {
	var req openapiv1.ChatCompletionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	ObserveModel(w, req.Model)
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages are required")
		return
	}
	if req.Stream != nil && *req.Stream {
		s.streamChatCompletion(w, r, req)
		return
	}

	adapter, err := s.router.AdapterForModel(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	in := proxy.ChatRequest{
		Model:    req.Model,
		Messages: make([]proxy.Message, 0, len(req.Messages)),
		Stream:   req.Stream != nil && *req.Stream,
	}
	for _, m := range req.Messages {
		in.Messages = append(in.Messages, proxy.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	promptTokens := estimateMessagesTokens(in.Messages)

	resp, err := adapter.Chat(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	text := strings.TrimSpace(resp.Text)
	ObserveTokenUsage(w, promptTokens, estimateTextTokens(text))
	finish := "stop"
	writeJSON(w, http.StatusOK, openapiv1.ChatCompletionsResponse{
		Id:     genID("chatcmpl"),
		Object: openapiv1.ChatCompletion,
		Model:  req.Model,
		Choices: []openapiv1.ChatChoice{
			{
				Index: 0,
				Message: openapiv1.ChatMessage{
					Role:    "assistant",
					Content: text,
				},
				FinishReason: &finish,
			},
		},
	})
}

func (s *Server) CreateResponse(w http.ResponseWriter, r *http.Request) {
	var req openapiv1.ResponsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	ObserveModel(w, req.Model)
	if req.Stream != nil && *req.Stream {
		s.streamResponse(w, r, req)
		return
	}

	adapter, err := s.router.AdapterForModel(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	var input any
	if req.Input != nil {
		if raw, marshalErr := req.Input.MarshalJSON(); marshalErr == nil {
			_ = json.Unmarshal(raw, &input)
		}
	}
	promptTokens := estimateInputTokens(input)

	resp, err := adapter.Respond(r.Context(), proxy.ResponsesRequest{
		Model:  req.Model,
		Input:  input,
		Stream: req.Stream != nil && *req.Stream,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	ObserveTokenUsage(w, promptTokens, estimateTextTokens(resp.Text)+estimateTextTokens(resp.Reasoning))

	output := make([]map[string]any, 0, 2)
	if strings.TrimSpace(resp.Reasoning) != "" {
		output = append(output, map[string]any{
			"id":     genID("rsn"),
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{
				{
					"type": "summary_text",
					"text": strings.TrimSpace(resp.Reasoning),
				},
			},
		})
	}
	output = append(output, map[string]any{
		"id":     genID("msg"),
		"type":   "message",
		"role":   "assistant",
		"status": "completed",
		"content": []map[string]any{
			{
				"type": "output_text",
				"text": resp.Text,
			},
		},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         genID("resp"),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      req.Model,
		"status":     "completed",
		"output":     output,
	})
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, req openapiv1.ChatCompletionsRequest) {
	adapter, err := s.router.AdapterForModel(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	sse, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	reqID := genID("chatcmpl")
	_ = sse.writeJSON(map[string]any{
		"id":     reqID,
		"object": "chat.completion.chunk",
		"model":  req.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{"role": "assistant"},
			},
		},
	})

	in := proxy.ChatRequest{
		Model:    req.Model,
		Messages: make([]proxy.Message, 0, len(req.Messages)),
		Stream:   true,
	}
	for _, m := range req.Messages {
		in.Messages = append(in.Messages, proxy.Message{Role: m.Role, Content: m.Content})
	}
	promptTokens := estimateMessagesTokens(in.Messages)
	var out strings.Builder

	_, err = adapter.ChatStream(ctx, in, func(delta string) error {
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		out.WriteString(delta)
		if writeErr := sse.writeJSON(map[string]any{
			"id":     reqID,
			"object": "chat.completion.chunk",
			"model":  req.Model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{"content": delta},
				},
			},
		}); writeErr != nil {
			cancel()
			return writeErr
		}
		return nil
	})
	if err != nil {
		_ = sse.writeJSON(map[string]any{
			"id":     reqID,
			"object": "error",
			"error": map[string]any{
				"type":    "upstream_error",
				"message": err.Error(),
			},
		})
		_ = sse.writeDone()
		return
	}
	ObserveTokenUsage(w, promptTokens, estimateTextTokens(out.String()))

	_ = sse.writeJSON(map[string]any{
		"id":     reqID,
		"object": "chat.completion.chunk",
		"model":  req.Model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
	})
	_ = sse.writeDone()
}

func (s *Server) streamResponse(w http.ResponseWriter, r *http.Request, req openapiv1.ResponsesRequest) {
	adapter, err := s.router.AdapterForModel(r.Context(), req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	sse, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	respID := genID("resp")
	createdAt := time.Now().Unix()
	_ = sse.writeJSON(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      req.Model,
			"status":     "in_progress",
			"output":     []any{},
		},
	})

	var input any
	if req.Input != nil {
		if raw, marshalErr := req.Input.MarshalJSON(); marshalErr == nil {
			_ = json.Unmarshal(raw, &input)
		}
	}
	promptTokens := estimateInputTokens(input)

	seq := int64(1)
	nextSeq := func() int64 {
		s := seq
		seq++
		return s
	}

	reasoningItemID := genID("rsn")
	messageItemID := genID("msg")
	reasoningIndex := int64(0)
	messageIndex := int64(0)
	reasoningStarted := false
	messageStarted := false
	var reasoningText strings.Builder
	var outputText strings.Builder
	var reasoningSummaryAdded bool

	startReasoning := func() error {
		if reasoningStarted {
			return nil
		}
		reasoningStarted = true
		if messageStarted {
			messageIndex = 1
		}
		if err := sse.writeJSON(map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": nextSeq(),
			"output_index":    reasoningIndex,
			"item": map[string]any{
				"id":      reasoningItemID,
				"type":    "reasoning",
				"status":  "in_progress",
				"summary": []any{},
			},
		}); err != nil {
			return err
		}
		if !reasoningSummaryAdded {
			reasoningSummaryAdded = true
			return sse.writeJSON(map[string]any{
				"type":            "response.reasoning_summary_part.added",
				"sequence_number": nextSeq(),
				"item_id":         reasoningItemID,
				"output_index":    reasoningIndex,
				"summary_index":   0,
				"part": map[string]any{
					"type": "summary_text",
					"text": "",
				},
			})
		}
		return nil
	}

	startMessage := func() error {
		if messageStarted {
			return nil
		}
		messageStarted = true
		if reasoningStarted {
			messageIndex = 1
		} else {
			messageIndex = 0
		}
		return sse.writeJSON(map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": nextSeq(),
			"output_index":    messageIndex,
			"item": map[string]any{
				"id":     messageItemID,
				"type":   "message",
				"role":   "assistant",
				"status": "in_progress",
				"content": []map[string]any{
					{"type": "output_text", "text": ""},
				},
			},
		})
	}

	emitReasoningDelta := func(delta string) error {
		if delta == "" {
			return nil
		}
		if err := startReasoning(); err != nil {
			return err
		}
		reasoningText.WriteString(delta)
		if err := sse.writeJSON(map[string]any{
			"type":            "response.reasoning_summary_text.delta",
			"sequence_number": nextSeq(),
			"item_id":         reasoningItemID,
			"output_index":    reasoningIndex,
			"summary_index":   0,
			"delta":           delta,
		}); err != nil {
			return err
		}
		return sse.writeJSON(map[string]any{
			"type":            "response.reasoning_text.delta",
			"sequence_number": nextSeq(),
			"item_id":         reasoningItemID,
			"output_index":    reasoningIndex,
			"content_index":   0,
			"delta":           delta,
		})
	}

	emitOutputDelta := func(delta string) error {
		if delta == "" {
			return nil
		}
		if err := startMessage(); err != nil {
			return err
		}
		outputText.WriteString(delta)
		return sse.writeJSON(map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": nextSeq(),
			"item_id":         messageItemID,
			"output_index":    messageIndex,
			"content_index":   0,
			"delta":           delta,
			"logprobs":        []any{},
		})
	}

	if eventAdapter, ok := adapter.(proxy.ResponsesEventAdapter); ok {
		_, err = eventAdapter.RespondStreamEvents(ctx, proxy.ResponsesRequest{
			Model:  req.Model,
			Input:  input,
			Stream: true,
		}, func(ev proxy.ResponseEvent) error {
			if ev.Kind == proxy.ResponseEventReasoning {
				if writeErr := emitReasoningDelta(ev.Delta); writeErr != nil {
					cancel()
					return writeErr
				}
				return nil
			}
			if writeErr := emitOutputDelta(ev.Delta); writeErr != nil {
				cancel()
				return writeErr
			}
			return nil
		})
	} else {
		_, err = adapter.RespondStream(ctx, proxy.ResponsesRequest{
			Model:  req.Model,
			Input:  input,
			Stream: true,
		}, func(delta string) error {
			if writeErr := emitOutputDelta(delta); writeErr != nil {
				cancel()
				return writeErr
			}
			return nil
		})
	}
	if err != nil {
		_ = sse.writeJSON(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "upstream_error",
				"message": err.Error(),
			},
		})
		_ = sse.writeDone()
		return
	}
	ObserveTokenUsage(w, promptTokens, estimateTextTokens(outputText.String())+estimateTextTokens(reasoningText.String()))

	if !messageStarted {
		_ = startMessage()
	}
	if reasoningStarted {
		reasoningFull := reasoningText.String()
		_ = sse.writeJSON(map[string]any{
			"type":            "response.reasoning_summary_text.done",
			"sequence_number": nextSeq(),
			"item_id":         reasoningItemID,
			"output_index":    reasoningIndex,
			"summary_index":   0,
			"text":            reasoningFull,
		})
		_ = sse.writeJSON(map[string]any{
			"type":            "response.reasoning_summary_part.done",
			"sequence_number": nextSeq(),
			"item_id":         reasoningItemID,
			"output_index":    reasoningIndex,
			"summary_index":   0,
			"part": map[string]any{
				"type": "summary_text",
				"text": reasoningFull,
			},
		})
		_ = sse.writeJSON(map[string]any{
			"type":            "response.reasoning_text.done",
			"sequence_number": nextSeq(),
			"item_id":         reasoningItemID,
			"output_index":    reasoningIndex,
			"content_index":   0,
			"text":            reasoningFull,
		})
		_ = sse.writeJSON(map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": nextSeq(),
			"output_index":    reasoningIndex,
			"item": map[string]any{
				"id":     reasoningItemID,
				"type":   "reasoning",
				"status": "completed",
				"summary": []map[string]any{
					{"type": "summary_text", "text": reasoningFull},
				},
			},
		})
	}

	outputFull := outputText.String()
	_ = sse.writeJSON(map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": nextSeq(),
		"item_id":         messageItemID,
		"output_index":    messageIndex,
		"content_index":   0,
		"text":            outputFull,
		"logprobs":        []any{},
	})
	_ = sse.writeJSON(map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": nextSeq(),
		"output_index":    messageIndex,
		"item": map[string]any{
			"id":     messageItemID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{
				{"type": "output_text", "text": outputFull},
			},
		},
	})

	outputItems := make([]any, 0, 2)
	if reasoningStarted {
		outputItems = append(outputItems, map[string]any{
			"id":     reasoningItemID,
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{
				{"type": "summary_text", "text": reasoningText.String()},
			},
		})
	}
	outputItems = append(outputItems, map[string]any{
		"id":     messageItemID,
		"type":   "message",
		"role":   "assistant",
		"status": "completed",
		"content": []map[string]any{
			{"type": "output_text", "text": outputFull},
		},
	})
	_ = sse.writeJSON(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      req.Model,
			"status":     "completed",
			"output":     outputItems,
		},
	})
	_ = sse.writeDone()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"type":    code,
			"message": message,
		},
	})
}

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported by response writer")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return &sseWriter{w: w, f: f}, nil
}

func (s *sseWriter) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sseWriter) writeDone() error {
	if _, err := fmt.Fprint(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func genID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func estimateMessagesTokens(messages []proxy.Message) uint64 {
	var total uint64
	for _, msg := range messages {
		total += estimateTextTokens(msg.Role)
		total += estimateTextTokens(msg.Content)
	}
	return total
}

func estimateInputTokens(input any) uint64 {
	if input == nil {
		return 0
	}
	if s, ok := input.(string); ok {
		return estimateTextTokens(s)
	}
	b, err := json.Marshal(input)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(b))
}

func estimateTextTokens(text string) uint64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := uint64(len([]rune(text)))
	approx := (runes + 3) / 4
	if approx == 0 {
		return 1
	}
	return approx
}

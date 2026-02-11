package api

import (
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
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages are required")
		return
	}
	if req.Stream != nil && *req.Stream {
		writeError(w, http.StatusNotImplemented, "not_implemented", "streaming will be enabled with CLI adapters")
		return
	}

	adapter, err := s.router.AdapterForModel(req.Model)
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

	resp, err := adapter.Chat(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	text := strings.TrimSpace(resp.Text)
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
	if req.Stream != nil && *req.Stream {
		writeError(w, http.StatusNotImplemented, "not_implemented", "streaming will be enabled with CLI adapters")
		return
	}

	adapter, err := s.router.AdapterForModel(req.Model)
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

	resp, err := adapter.Respond(r.Context(), proxy.ResponsesRequest{
		Model:  req.Model,
		Input:  input,
		Stream: req.Stream != nil && *req.Stream,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, openapiv1.ResponsesResponse{
		Id:     genID("resp"),
		Object: openapiv1.Response,
		Model:  req.Model,
		Output: []openapiv1.ResponsesOutputItem{
			{
				Id:   genID("out"),
				Type: "message",
				Content: &[]openapiv1.ResponsesOutputText{
					{
						Type: openapiv1.OutputText,
						Text: resp.Text,
					},
				},
			},
		},
	})
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

func genID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

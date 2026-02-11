package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-proxy/internal/proxy"
)

type streamingTestAdapter struct {
	model  string
	deltas []string
	events []proxy.ResponseEvent
}

func (a *streamingTestAdapter) SupportsModel(_ context.Context, model string) (bool, error) {
	return model == a.model, nil
}

func (a *streamingTestAdapter) ListModels(_ context.Context) ([]proxy.Model, error) {
	return []proxy.Model{{ID: a.model, Backend: proxy.BackendClaude}}, nil
}

func (a *streamingTestAdapter) Chat(_ context.Context, req proxy.ChatRequest) (proxy.ChatResponse, error) {
	return proxy.ChatResponse{Model: req.Model, Text: strings.Join(a.deltas, "")}, nil
}

func (a *streamingTestAdapter) ChatStream(_ context.Context, req proxy.ChatRequest, onDelta func(string) error) (proxy.ChatResponse, error) {
	for _, delta := range a.deltas {
		if err := onDelta(delta); err != nil {
			return proxy.ChatResponse{}, err
		}
	}
	return proxy.ChatResponse{Model: req.Model, Text: strings.Join(a.deltas, "")}, nil
}

func (a *streamingTestAdapter) Respond(_ context.Context, req proxy.ResponsesRequest) (proxy.ResponsesResponse, error) {
	return proxy.ResponsesResponse{Model: req.Model, Text: "ok"}, nil
}

func (a *streamingTestAdapter) RespondStream(_ context.Context, req proxy.ResponsesRequest, onDelta func(string) error) (proxy.ResponsesResponse, error) {
	for _, delta := range a.deltas {
		if err := onDelta(delta); err != nil {
			return proxy.ResponsesResponse{}, err
		}
	}
	return proxy.ResponsesResponse{Model: req.Model, Text: strings.Join(a.deltas, "")}, nil
}

func (a *streamingTestAdapter) RespondStreamEvents(_ context.Context, req proxy.ResponsesRequest, onEvent func(proxy.ResponseEvent) error) (proxy.ResponsesResponse, error) {
	for _, ev := range a.events {
		if err := onEvent(ev); err != nil {
			return proxy.ResponsesResponse{}, err
		}
	}
	return proxy.ResponsesResponse{Model: req.Model, Text: "done"}, nil
}

func TestStreamChatCompletionPreservesWhitespaceDeltas(t *testing.T) {
	adapter := &streamingTestAdapter{model: "m1", deltas: []string{"hello", " ", "world"}}
	s := NewServer(proxy.NewRouter(adapter, &streamingTestAdapter{model: "m2"}))

	body := []byte(`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.CreateChatCompletion(w, r)

	events := decodeSSEEvents(t, w.Body.String())
	var got []string
	for _, ev := range events {
		choices, ok := ev["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		if content, ok := delta["content"].(string); ok {
			got = append(got, content)
		}
	}
	if strings.Join(got, "") != "hello world" {
		t.Fatalf("expected streamed content to preserve spaces, got %q", strings.Join(got, ""))
	}
}

func TestStreamResponseKeepsMessageOutputIndexStable(t *testing.T) {
	adapter := &streamingTestAdapter{
		model: "m1",
		events: []proxy.ResponseEvent{
			{Kind: proxy.ResponseEventOutput, Delta: "Hello"},
			{Kind: proxy.ResponseEventReasoning, Delta: " thinking"},
			{Kind: proxy.ResponseEventOutput, Delta: " world"},
		},
	}
	s := NewServer(proxy.NewRouter(adapter, &streamingTestAdapter{model: "m2"}))

	body := []byte(`{"model":"m1","stream":true,"input":"hi"}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.CreateResponse(w, r)

	events := decodeSSEEvents(t, w.Body.String())
	messageID := ""
	messageIndex := -1.0
	for _, ev := range events {
		typ, _ := ev["type"].(string)
		if typ != "response.output_item.added" {
			continue
		}
		item, ok := ev["item"].(map[string]any)
		if !ok {
			continue
		}
		if itemType, _ := item["type"].(string); itemType == "message" {
			messageID, _ = item["id"].(string)
			messageIndex, _ = ev["output_index"].(float64)
			break
		}
	}
	if messageID == "" {
		t.Fatalf("message item not found in stream")
	}

	for _, ev := range events {
		itemID := ""
		if v, ok := ev["item_id"].(string); ok {
			itemID = v
		} else if item, ok := ev["item"].(map[string]any); ok {
			if v, ok := item["id"].(string); ok {
				itemID = v
			}
		}
		if itemID != messageID {
			continue
		}
		if idx, ok := ev["output_index"].(float64); ok && idx != messageIndex {
			t.Fatalf("message output_index changed from %v to %v on event %q", messageIndex, idx, ev["type"])
		}
	}
}

func decodeSSEEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	lines := strings.Split(body, "\n")
	events := make([]map[string]any, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			t.Fatalf("failed to parse SSE payload %q: %v", payload, err)
		}
		events = append(events, evt)
	}
	return events
}

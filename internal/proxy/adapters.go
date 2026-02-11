package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type ClaudeAdapter struct {
	bin       string
	models    []string
	checkAuth sync.Once
	authErr   error
}

func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{
		bin:    envOrDefault("CLAUDE_BIN", "claude"),
		models: parseClaudeModels(os.Getenv("CLAUDE_MODELS")),
	}
}

func parseClaudeModels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"haiku", "sonnet", "opus"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{"haiku", "sonnet", "opus"}
	}
	return out
}

func (a *ClaudeAdapter) ensureSubscriptionMode() error {
	a.checkAuth.Do(func() {
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
			a.authErr = errors.New("ANTHROPIC_API_KEY is set; refusing API-key mode for Claude adapter")
		}
	})
	return a.authErr
}

func (a *ClaudeAdapter) ListModels(ctx context.Context) ([]Model, error) {
	if err := a.ensureSubscriptionMode(); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(a.models))
	for _, m := range a.models {
		out = append(out, Model{ID: m, Backend: BackendClaude})
	}
	return out, nil
}

func (a *ClaudeAdapter) SupportsModel(_ context.Context, model string) (bool, error) {
	model = strings.TrimSpace(model)
	for _, m := range a.models {
		if m == model {
			return true, nil
		}
	}
	return false, nil
}

func (a *ClaudeAdapter) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if err := a.ensureSubscriptionMode(); err != nil {
		return ChatResponse{}, err
	}
	model := req.Model
	prompt := buildChatPrompt(req.Messages)
	out, err := a.runClaudeText(ctx, model, prompt)
	if err != nil {
		return ChatResponse{}, err
	}
	return ChatResponse{
		Model: req.Model,
		Text:  strings.TrimSpace(out),
	}, nil
}

func (a *ClaudeAdapter) ChatStream(ctx context.Context, req ChatRequest, onDelta func(string) error) (ChatResponse, error) {
	if err := a.ensureSubscriptionMode(); err != nil {
		return ChatResponse{}, err
	}
	model := req.Model
	prompt := buildChatPrompt(req.Messages)

	text, emitted, err := a.runClaudeStream(ctx, model, prompt, onDelta)
	if err != nil {
		fallback, fbErr := a.runClaudeText(ctx, model, prompt)
		if fbErr != nil {
			return ChatResponse{}, err
		}
		text = strings.TrimSpace(fallback)
		if !emitted && onDelta != nil && text != "" {
			if cbErr := onDelta(text); cbErr != nil {
				return ChatResponse{}, cbErr
			}
		}
		return ChatResponse{Model: req.Model, Text: text}, nil
	}
	if strings.TrimSpace(text) == "" {
		fallback, fbErr := a.runClaudeText(ctx, model, prompt)
		if fbErr != nil {
			return ChatResponse{}, fbErr
		}
		text = strings.TrimSpace(fallback)
		if !emitted && onDelta != nil && text != "" {
			if err := onDelta(text); err != nil {
				return ChatResponse{}, err
			}
		}
	}
	return ChatResponse{Model: req.Model, Text: text}, nil
}

func (a *ClaudeAdapter) Respond(ctx context.Context, req ResponsesRequest) (ResponsesResponse, error) {
	if err := a.ensureSubscriptionMode(); err != nil {
		return ResponsesResponse{}, err
	}
	model := req.Model
	prompt := buildResponsesPrompt(req.Input)
	out, err := a.runClaudeText(ctx, model, prompt)
	if err != nil {
		return ResponsesResponse{}, err
	}
	return ResponsesResponse{
		Model:     req.Model,
		Text:      strings.TrimSpace(out),
		Reasoning: "",
	}, nil
}

func (a *ClaudeAdapter) RespondStream(ctx context.Context, req ResponsesRequest, onDelta func(string) error) (ResponsesResponse, error) {
	if err := a.ensureSubscriptionMode(); err != nil {
		return ResponsesResponse{}, err
	}
	model := req.Model
	prompt := buildResponsesPrompt(req.Input)

	text, emitted, err := a.runClaudeStream(ctx, model, prompt, onDelta)
	if err != nil {
		fallback, fbErr := a.runClaudeText(ctx, model, prompt)
		if fbErr != nil {
			return ResponsesResponse{}, err
		}
		text = strings.TrimSpace(fallback)
		if !emitted && onDelta != nil && text != "" {
			if cbErr := onDelta(text); cbErr != nil {
				return ResponsesResponse{}, cbErr
			}
		}
		return ResponsesResponse{Model: req.Model, Text: text}, nil
	}
	if strings.TrimSpace(text) == "" {
		fallback, fbErr := a.runClaudeText(ctx, model, prompt)
		if fbErr != nil {
			return ResponsesResponse{}, fbErr
		}
		text = strings.TrimSpace(fallback)
		if !emitted && onDelta != nil && text != "" {
			if err := onDelta(text); err != nil {
				return ResponsesResponse{}, err
			}
		}
	}
	return ResponsesResponse{Model: req.Model, Text: text, Reasoning: ""}, nil
}

func (a *ClaudeAdapter) RespondStreamEvents(ctx context.Context, req ResponsesRequest, onEvent func(ResponseEvent) error) (ResponsesResponse, error) {
	resp, err := a.RespondStream(ctx, req, func(delta string) error {
		if onEvent == nil {
			return nil
		}
		return onEvent(ResponseEvent{Kind: ResponseEventOutput, Delta: delta})
	})
	if err != nil {
		return ResponsesResponse{}, err
	}
	return resp, nil
}

func (a *ClaudeAdapter) runClaudeText(ctx context.Context, model string, prompt string) (string, error) {
	args := []string{
		"-p",
		"--output-format", "text",
		"--model", model,
	}
	if YOLOEnabled() {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (a *ClaudeAdapter) runClaudeStream(ctx context.Context, model string, prompt string, onDelta func(string) error) (string, bool, error) {
	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--model", model,
	}
	if YOLOEnabled() {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", false, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out strings.Builder
	emitted := false
	lastByIndex := map[int]string{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		delta, ok := extractClaudeDelta(line, lastByIndex)
		if !ok || delta == "" {
			continue
		}
		out.WriteString(delta)
		emitted = true
		if onDelta != nil {
			if err := onDelta(delta); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return "", emitted, err
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", emitted, scanErr
	}
	if err := cmd.Wait(); err != nil {
		return "", emitted, fmt.Errorf("claude stream command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), emitted, nil
}

func extractClaudeDelta(line string, lastByIndex map[int]string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return "", false
	}

	typ := stringVal(raw["type"])
	switch typ {
	case "content_block_delta":
		if d, ok := raw["delta"].(map[string]any); ok {
			if t := stringVal(d["text"]); t != "" {
				return t, true
			}
		}
	case "content_block_start":
		if cb, ok := raw["content_block"].(map[string]any); ok {
			if t := stringVal(cb["text"]); t != "" {
				return t, true
			}
		}
	case "message_delta":
		if d, ok := raw["delta"].(map[string]any); ok {
			if t := stringVal(d["text"]); t != "" {
				return t, true
			}
		}
	}

	// Fallback parser for event shapes that expose growing partial content.
	if msg, ok := raw["message"].(map[string]any); ok {
		if content, ok := msg["content"].([]any); ok {
			for idx, it := range content {
				item, ok := it.(map[string]any)
				if !ok {
					continue
				}
				full := stringVal(item["text"])
				if full == "" {
					continue
				}
				prev := lastByIndex[idx]
				if strings.HasPrefix(full, prev) {
					delta := strings.TrimPrefix(full, prev)
					lastByIndex[idx] = full
					if delta != "" {
						return delta, true
					}
				} else if prev == "" {
					lastByIndex[idx] = full
					return full, true
				}
			}
		}
	}

	return "", false
}

func stringVal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

type CodexAdapter struct {
	bin       string
	checkAuth sync.Once
	authErr   error
}

func NewCodexAdapter() *CodexAdapter {
	return &CodexAdapter{bin: envOrDefault("CODEX_BIN", "codex")}
}

func (a *CodexAdapter) ensureSubscriptionMode(ctx context.Context) error {
	a.checkAuth.Do(func() {
		home, _ := os.UserHomeDir()
		if home != "" {
			authFile := filepath.Join(home, ".codex", "auth.json")
			data, err := os.ReadFile(authFile)
			if err == nil {
				var state struct {
					AuthMode string `json:"auth_mode"`
				}
				if json.Unmarshal(data, &state) == nil && strings.EqualFold(strings.TrimSpace(state.AuthMode), "chatgpt") {
					return
				}
			}
		}

		cmd := exec.CommandContext(ctx, a.bin, "login", "status")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			a.authErr = fmt.Errorf("failed to check codex login status: %w: %s", err, strings.TrimSpace(stderr.String()))
			return
		}
		status := strings.ToLower(string(out))
		if !strings.Contains(status, "chatgpt") {
			a.authErr = fmt.Errorf("codex auth mode is not ChatGPT subscription: %s", strings.TrimSpace(string(out)))
		}
	})
	return a.authErr
}

func (a *CodexAdapter) ListModels(ctx context.Context) ([]Model, error) {
	if err := a.ensureSubscriptionMode(ctx); err != nil {
		return nil, err
	}

	client, err := newCodexRPCClient(ctx, a.bin)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.initialize(); err != nil {
		return nil, err
	}

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.call("model/list", map[string]any{}, &resp, nil); err != nil {
		return nil, err
	}

	if len(resp.Data) == 0 {
		return nil, errors.New("codex returned no models")
	}

	out := make([]Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, Model{
			ID:      m.ID,
			Backend: BackendCodex,
		})
	}
	return out, nil
}

func (a *CodexAdapter) SupportsModel(ctx context.Context, model string) (bool, error) {
	models, err := a.ListModels(ctx)
	if err != nil {
		return false, err
	}
	for _, m := range models {
		if m.ID == model {
			return true, nil
		}
	}
	return false, nil
}

func (a *CodexAdapter) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if err := a.ensureSubscriptionMode(ctx); err != nil {
		return ChatResponse{}, err
	}
	turn, err := a.runTurnStructured(ctx, req.Model, buildChatPrompt(req.Messages), nil)
	if err != nil {
		return ChatResponse{}, err
	}
	return ChatResponse{
		Model: req.Model,
		Text:  turn.Output,
	}, nil
}

func (a *CodexAdapter) ChatStream(ctx context.Context, req ChatRequest, onDelta func(string) error) (ChatResponse, error) {
	if err := a.ensureSubscriptionMode(ctx); err != nil {
		return ChatResponse{}, err
	}
	turn, err := a.runTurnStructured(ctx, req.Model, buildChatPrompt(req.Messages), nil)
	if err != nil {
		return ChatResponse{}, err
	}
	if onDelta != nil && strings.TrimSpace(turn.Output) != "" {
		if err := onDelta(turn.Output); err != nil {
			return ChatResponse{}, err
		}
	}
	return ChatResponse{
		Model: req.Model,
		Text:  turn.Output,
	}, nil
}

func (a *CodexAdapter) Respond(ctx context.Context, req ResponsesRequest) (ResponsesResponse, error) {
	if err := a.ensureSubscriptionMode(ctx); err != nil {
		return ResponsesResponse{}, err
	}
	turn, err := a.runTurnStructured(ctx, req.Model, buildResponsesPrompt(req.Input), nil)
	if err != nil {
		return ResponsesResponse{}, err
	}
	return ResponsesResponse{
		Model:     req.Model,
		Text:      turn.Output,
		Reasoning: turn.Reasoning,
	}, nil
}

func (a *CodexAdapter) RespondStream(ctx context.Context, req ResponsesRequest, onDelta func(string) error) (ResponsesResponse, error) {
	if err := a.ensureSubscriptionMode(ctx); err != nil {
		return ResponsesResponse{}, err
	}
	turn, err := a.runTurnStructured(ctx, req.Model, buildResponsesPrompt(req.Input), nil)
	if err != nil {
		return ResponsesResponse{}, err
	}
	if onDelta != nil && strings.TrimSpace(turn.Output) != "" {
		if err := onDelta(turn.Output); err != nil {
			return ResponsesResponse{}, err
		}
	}
	return ResponsesResponse{
		Model:     req.Model,
		Text:      turn.Output,
		Reasoning: turn.Reasoning,
	}, nil
}

func (a *CodexAdapter) RespondStreamEvents(ctx context.Context, req ResponsesRequest, onEvent func(ResponseEvent) error) (ResponsesResponse, error) {
	if err := a.ensureSubscriptionMode(ctx); err != nil {
		return ResponsesResponse{}, err
	}
	turn, err := a.runTurnStructured(ctx, req.Model, buildResponsesPrompt(req.Input), onEvent)
	if err != nil {
		return ResponsesResponse{}, err
	}
	return ResponsesResponse{
		Model:     req.Model,
		Text:      turn.Output,
		Reasoning: turn.Reasoning,
	}, nil
}

type codexTurnResult struct {
	Output    string
	Reasoning string
}

type codexTurnState struct {
	currentAgent strings.Builder
	agentMsgs    []string
	reasoning    strings.Builder
	inAgentMsg   bool
}

func (s *codexTurnState) appendReasoning(delta string) {
	if strings.TrimSpace(delta) == "" {
		return
	}
	s.reasoning.WriteString(delta)
}

func (s *codexTurnState) appendAgentDelta(delta string) {
	s.inAgentMsg = true
	s.currentAgent.WriteString(delta)
}

func (s *codexTurnState) completeAgentMessage() {
	msg := strings.TrimSpace(s.currentAgent.String())
	if msg != "" {
		s.agentMsgs = append(s.agentMsgs, msg)
	}
	s.currentAgent.Reset()
	s.inAgentMsg = false
}

func (s *codexTurnState) finalize() {
	if s.inAgentMsg || s.currentAgent.Len() > 0 {
		s.completeAgentMessage()
	}
}

func (s *codexTurnState) result(lastAgentMessage string) codexTurnResult {
	s.finalize()
	output := strings.TrimSpace(lastAgentMessage)
	if output == "" && len(s.agentMsgs) > 0 {
		output = strings.TrimSpace(s.agentMsgs[len(s.agentMsgs)-1])
	}

	reasoningParts := make([]string, 0, len(s.agentMsgs))
	for i := 0; i+1 < len(s.agentMsgs); i++ {
		if strings.TrimSpace(s.agentMsgs[i]) != "" {
			reasoningParts = append(reasoningParts, strings.TrimSpace(s.agentMsgs[i]))
		}
	}
	reasoning := strings.TrimSpace(s.reasoning.String())
	if len(reasoningParts) > 0 {
		progress := strings.Join(reasoningParts, "\n\n")
		if reasoning != "" {
			reasoning = reasoning + "\n\n" + progress
		} else {
			reasoning = progress
		}
	}
	return codexTurnResult{
		Output:    output,
		Reasoning: strings.TrimSpace(reasoning),
	}
}

func (a *CodexAdapter) runTurnStructured(ctx context.Context, model string, prompt string, onEvent func(ResponseEvent) error) (codexTurnResult, error) {
	client, err := newCodexRPCClient(ctx, a.bin)
	if err != nil {
		return codexTurnResult{}, err
	}
	defer client.Close()

	if err := client.initialize(); err != nil {
		return codexTurnResult{}, err
	}

	var threadStart struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.call("thread/start", map[string]any{
		"model":     model,
		"ephemeral": true,
	}, &threadStart, nil); err != nil {
		return codexTurnResult{}, err
	}
	if threadStart.Thread.ID == "" {
		return codexTurnResult{}, errors.New("codex returned empty thread id")
	}

	var (
		lastAgentMessage string
		callbackErr      error
		state            codexTurnState
		emittedReasoning bool
	)

	emit := func(kind ResponseEventKind, delta string) {
		if onEvent == nil || callbackErr != nil || delta == "" {
			return
		}
		if err := onEvent(ResponseEvent{Kind: kind, Delta: delta}); err != nil {
			callbackErr = err
		}
	}

	turnCompleted := false
	notify := func(msg codexRPCMessage) {
		switch msg.Method {
		case "turn/completed":
			turnCompleted = true
		case "item/reasoning/summaryTextDelta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(msg.Params, &payload) == nil && payload.Delta != "" {
				state.appendReasoning(payload.Delta)
				emittedReasoning = true
				emit(ResponseEventReasoning, payload.Delta)
			}
		case "item/agentMessage/delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(msg.Params, &payload) == nil && payload.Delta != "" {
				state.appendAgentDelta(payload.Delta)
			}
		case "item/started":
			var payload struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(msg.Params, &payload) == nil {
				if strings.EqualFold(payload.Item.Type, "agentMessage") {
					// New assistant message: close previous if it never got an explicit completed event.
					if state.currentAgent.Len() > 0 {
						state.completeAgentMessage()
					}
					state.inAgentMsg = true
				}
			}
		case "item/completed":
			var payload struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(msg.Params, &payload) == nil {
				if strings.EqualFold(payload.Item.Type, "agentMessage") {
					state.completeAgentMessage()
				}
			}
		case "codex/event/task_complete":
			var payload struct {
				Msg struct {
					LastAgentMessage string `json:"last_agent_message"`
				} `json:"msg"`
			}
			if json.Unmarshal(msg.Params, &payload) == nil {
				lastAgentMessage = payload.Msg.LastAgentMessage
			}
		}
	}

	var turnResp map[string]any
	err = client.call("turn/start", map[string]any{
		"threadId": threadStart.Thread.ID,
		"model":    model,
		"input": []map[string]any{
			{
				"type": "text",
				"text": prompt,
			},
		},
	}, &turnResp, notify)
	if err != nil {
		return codexTurnResult{}, err
	}

	if err := waitForTurnCompleted(ctx, client.msgs, notify, turnCompleted); err != nil {
		return codexTurnResult{}, err
	}
	if callbackErr != nil {
		return codexTurnResult{}, callbackErr
	}

	result := state.result(lastAgentMessage)
	if result.Output == "" {
		return codexTurnResult{}, errors.New("codex returned empty assistant output")
	}
	if !emittedReasoning && strings.TrimSpace(result.Reasoning) != "" {
		emit(ResponseEventReasoning, result.Reasoning)
	}
	emit(ResponseEventOutput, result.Output)
	if callbackErr != nil {
		return codexTurnResult{}, callbackErr
	}
	return result, nil
}

func waitForTurnCompleted(ctx context.Context, msgs <-chan codexRPCMessage, notify func(codexRPCMessage), alreadyCompleted bool) error {
	if alreadyCompleted {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			if notify != nil {
				notify(msg)
			}
			if msg.Method == "turn/completed" {
				return nil
			}
		}
	}
}

type Router struct {
	claude Adapter
	codex  Adapter
}

func NewRouter(claude Adapter, codex Adapter) *Router {
	return &Router{claude: claude, codex: codex}
}

type modelSupporter interface {
	SupportsModel(context.Context, string) (bool, error)
}

func (r *Router) AdapterForModel(ctx context.Context, model string) (Adapter, error) {
	if s, ok := r.claude.(modelSupporter); ok {
		supported, err := s.SupportsModel(ctx, model)
		if err != nil {
			return nil, fmt.Errorf("failed checking Claude models: %w", err)
		}
		if supported {
			return r.claude, nil
		}
	}
	if s, ok := r.codex.(modelSupporter); ok {
		supported, err := s.SupportsModel(ctx, model)
		if err != nil {
			return nil, fmt.Errorf("failed checking Codex models: %w", err)
		}
		if supported {
			return r.codex, nil
		}
	}
	return nil, fmt.Errorf("unsupported model id: %s", model)
}

func (r *Router) ListModels(ctx context.Context) ([]Model, error) {
	claudeModels, err := r.claude.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	codexModels, err := r.codex.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(claudeModels)+len(codexModels))
	out = append(out, claudeModels...)
	out = append(out, codexModels...)
	return out, nil
}

type codexRPCClient struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	msgs   chan codexRPCMessage
	stderr bytes.Buffer
	id     atomic.Int64
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newCodexRPCClient(ctx context.Context, bin string) (*codexRPCClient, error) {
	args := []string{"app-server"}
	if YOLOEnabled() {
		args = []string{"--dangerously-bypass-approvals-and-sandbox", "app-server"}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	client := &codexRPCClient{
		cmd:   cmd,
		stdin: bufio.NewWriter(stdinPipe),
		msgs:  make(chan codexRPCMessage, 256),
	}
	cmd.Stderr = &client.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	go func() {
		defer close(client.msgs)
		for scanner.Scan() {
			var msg codexRPCMessage
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			client.msgs <- msg
		}
	}()

	return client, nil
}

func (c *codexRPCClient) initialize() error {
	var resp map[string]any
	return c.call("initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "llm-proxy",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}, &resp, nil)
}

func (c *codexRPCClient) call(method string, params any, out any, onNotify func(codexRPCMessage)) error {
	id := c.id.Add(1)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      fmt.Sprintf("%d", id),
		"method":  method,
		"params":  params,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(line); err != nil {
		return err
	}
	if err := c.stdin.WriteByte('\n'); err != nil {
		return err
	}
	if err := c.stdin.Flush(); err != nil {
		return err
	}

	for msg := range c.msgs {
		if len(msg.ID) == 0 {
			if onNotify != nil {
				onNotify(msg)
			}
			continue
		}

		var gotID string
		if err := json.Unmarshal(msg.ID, &gotID); err != nil {
			continue
		}
		if gotID != fmt.Sprintf("%d", id) {
			if onNotify != nil && msg.Method != "" {
				onNotify(msg)
			}
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("codex RPC error on %s: (%d) %s", method, msg.Error.Code, msg.Error.Message)
		}
		if out == nil {
			return nil
		}
		if len(msg.Result) == 0 {
			return nil
		}
		return json.Unmarshal(msg.Result, out)
	}

	stderr := strings.TrimSpace(c.stderr.String())
	if stderr == "" {
		stderr = "unknown codex app-server failure"
	}
	return fmt.Errorf("codex app-server stream ended: %s", stderr)
}

func (c *codexRPCClient) Close() {
	_ = c.stdin.Flush()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

func buildChatPrompt(messages []Message) string {
	var b strings.Builder
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		b.WriteString("[")
		b.WriteString(role)
		b.WriteString("] ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func buildResponsesPrompt(input any) string {
	switch v := input.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

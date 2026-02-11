package proxy

import (
	"context"
	"fmt"
)

type ClaudeAdapter struct{}

func (a *ClaudeAdapter) ListModels(_ context.Context) ([]Model, error) {
	return []Model{
		{ID: "claude/opus", Backend: BackendClaude},
		{ID: "claude/sonnet", Backend: BackendClaude},
	}, nil
}

func (a *ClaudeAdapter) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	return ChatResponse{
		Model: req.Model,
		Text:  "Claude adapter not wired yet",
	}, nil
}

func (a *ClaudeAdapter) Respond(_ context.Context, req ResponsesRequest) (ResponsesResponse, error) {
	return ResponsesResponse{
		Model: req.Model,
		Text:  "Claude adapter not wired yet",
	}, nil
}

type CodexAdapter struct{}

func (a *CodexAdapter) ListModels(_ context.Context) ([]Model, error) {
	return []Model{
		{ID: "codex/gpt-5.3-codex", Backend: BackendCodex},
	}, nil
}

func (a *CodexAdapter) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	return ChatResponse{
		Model: req.Model,
		Text:  "Codex adapter not wired yet",
	}, nil
}

func (a *CodexAdapter) Respond(_ context.Context, req ResponsesRequest) (ResponsesResponse, error) {
	return ResponsesResponse{
		Model: req.Model,
		Text:  "Codex adapter not wired yet",
	}, nil
}

type Router struct {
	claude Adapter
	codex  Adapter
}

func NewRouter(claude Adapter, codex Adapter) *Router {
	return &Router{claude: claude, codex: codex}
}

func (r *Router) AdapterForModel(model string) (Adapter, error) {
	switch {
	case len(model) >= 7 && model[:7] == "claude/":
		return r.claude, nil
	case len(model) >= 6 && model[:6] == "codex/":
		return r.codex, nil
	default:
		return nil, fmt.Errorf("unsupported model namespace: %s", model)
	}
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

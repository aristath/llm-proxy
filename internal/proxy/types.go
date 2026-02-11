package proxy

import "context"

type Backend string

const (
	BackendClaude Backend = "claude"
	BackendCodex  Backend = "codex"
)

type Model struct {
	ID      string
	Backend Backend
}

type Message struct {
	Role    string
	Content string
}

type ChatRequest struct {
	Model    string
	Messages []Message
	Stream   bool
}

type ChatResponse struct {
	Model string
	Text  string
}

type ResponsesRequest struct {
	Model  string
	Input  any
	Stream bool
}

type ResponsesResponse struct {
	Model string
	Text  string
}

type Adapter interface {
	ListModels(context.Context) ([]Model, error)
	Chat(context.Context, ChatRequest) (ChatResponse, error)
	ChatStream(context.Context, ChatRequest, func(string) error) (ChatResponse, error)
	Respond(context.Context, ResponsesRequest) (ResponsesResponse, error)
	RespondStream(context.Context, ResponsesRequest, func(string) error) (ResponsesResponse, error)
}

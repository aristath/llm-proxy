# llm-proxy

`llm-proxy` is a local Go proxy that wraps the Claude CLI and Codex CLI behind an OpenAI-compatible HTTP API.

It is designed for local tooling (like Crush) so you can use standard OpenAI endpoints while routing requests to CLI agents.

## Features

- OpenAI-compatible endpoints:
  - `GET /v1/models`
  - `POST /v1/chat/completions`
  - `POST /v1/responses`
- Streaming support for chat completions and responses (SSE)
- Claude + Codex model routing by model ID
- Integrated Bubble Tea TUI for live monitoring
- Optional YOLO mode toggle for upstream CLI permission bypass flags
- Per-model usage metrics in TUI:
  - requests
  - token estimates
  - avg response time
  - avg tokens per call
  - avg tokens/sec

## Requirements

- Go 1.24+
- Claude CLI installed and authenticated with subscription mode
- Codex CLI installed and authenticated with ChatGPT subscription mode

By default, the proxy expects:

- `claude` on PATH
- `codex` on PATH

## Build

```bash
go build -o llm-proxy ./cmd/llm-proxy
```

## Run

### TUI mode (default)

```bash
./llm-proxy
```

### Headless mode

```bash
./llm-proxy --headless
```

## Flags

- `--addr` listen address (default `:8080`)
- `--headless` disable TUI
- `--yolo` enable YOLO mode

## Environment variables

- `ADDR` (default `:8080`)
- `LLM_PROXY_HEADLESS=1` run without TUI
- `LLM_PROXY_YOLO=1` enable YOLO at startup
- `CLAUDE_BIN` override Claude binary path/name
- `CODEX_BIN` override Codex binary path/name
- `CLAUDE_MODELS` comma-separated models exposed for Claude (default: `haiku,sonnet,opus`)

## TUI controls

- `y`: toggle YOLO mode
- `q` or `ctrl+c`: quit (and stop server)

## API notes

- No auth layer is implemented (intended for local use).
- Responses include reasoning/output events when available from adapter streams.
- Token metrics are estimated heuristically (not provider token accounting).
- Model IDs are raw IDs (no `claude/` or `codex/` prefixes).

## Example: use as a Crush provider

`~/.config/crush/crush.json`:

```json
{
  "providers": {
    "llm-proxy": {
      "name": "llm-proxy",
      "type": "openai",
      "base_url": "http://127.0.0.1:8080/v1/",
      "timeout": 300,
      "models": [
        { "name": "Claude Sonnet", "id": "sonnet" },
        { "name": "GPT-5.2 Codex", "id": "gpt-5.2-codex" }
      ]
    }
  }
}
```

## Project layout

- `cmd/llm-proxy/main.go` entrypoint
- `internal/api` HTTP server + metrics
- `internal/proxy` CLI adapters + routing
- `internal/tui` terminal dashboard
- `openapi/openai.yaml` API schema source


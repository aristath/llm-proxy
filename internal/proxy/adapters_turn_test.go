package proxy

import (
	"context"
	"testing"
	"time"
)

func TestWaitForTurnCompletedReturnsImmediatelyWhenAlreadyObserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msgs := make(chan codexRPCMessage)
	if err := waitForTurnCompleted(ctx, msgs, nil, true); err != nil {
		t.Fatalf("expected immediate success, got error: %v", err)
	}
}

func TestWaitForTurnCompletedConsumesUntilCompleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msgs := make(chan codexRPCMessage, 2)
	msgs <- codexRPCMessage{Method: "item/agentMessage/delta"}
	msgs <- codexRPCMessage{Method: "turn/completed"}
	close(msgs)

	seen := make([]string, 0, 2)
	err := waitForTurnCompleted(ctx, msgs, func(msg codexRPCMessage) {
		seen = append(seen, msg.Method)
	}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(seen))
	}
	if seen[0] != "item/agentMessage/delta" || seen[1] != "turn/completed" {
		t.Fatalf("unexpected notifications order: %#v", seen)
	}
}

func TestExtractClaudeEventParsesThinkingDelta(t *testing.T) {
	line := `{"type":"content_block_delta","delta":{"thinking":"working through it"}}`
	ev, ok := extractClaudeEvent(line, map[string]string{})
	if !ok {
		t.Fatalf("expected event")
	}
	if ev.Kind != ResponseEventReasoning {
		t.Fatalf("expected reasoning event, got %q", ev.Kind)
	}
	if ev.Delta != "working through it" {
		t.Fatalf("unexpected delta: %q", ev.Delta)
	}
}

func TestExtractClaudeEventParsesOutputDelta(t *testing.T) {
	line := `{"type":"content_block_delta","delta":{"text":"hello"}}`
	ev, ok := extractClaudeEvent(line, map[string]string{})
	if !ok {
		t.Fatalf("expected event")
	}
	if ev.Kind != ResponseEventOutput {
		t.Fatalf("expected output event, got %q", ev.Kind)
	}
	if ev.Delta != "hello" {
		t.Fatalf("unexpected delta: %q", ev.Delta)
	}
}

func TestExtractClaudeEventParsesWrappedStreamEventDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`
	ev, ok := extractClaudeEvent(line, map[string]string{})
	if !ok {
		t.Fatalf("expected event")
	}
	if ev.Kind != ResponseEventOutput {
		t.Fatalf("expected output event, got %q", ev.Kind)
	}
	if ev.Delta != "hello" {
		t.Fatalf("unexpected delta: %q", ev.Delta)
	}
}

func TestExtractClaudeEventResetsWhenTextChangesNonPrefix(t *testing.T) {
	cache := map[string]string{"0:output": "I'll review the codebase"}
	line := `{"type":"legacy","message":{"content":[{"type":"text","text":"Based on my review, here are the issues"}]}}`
	ev, ok := extractClaudeEvent(line, cache)
	if !ok {
		t.Fatalf("expected event")
	}
	if ev.Kind != ResponseEventOutput {
		t.Fatalf("expected output event, got %q", ev.Kind)
	}
	if ev.Delta != "Based on my review, here are the issues" {
		t.Fatalf("unexpected delta: %q", ev.Delta)
	}
}

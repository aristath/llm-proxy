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

package agui

import (
	"context"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/runner"
)

func TestInMemorySuspendStoreRoundTrip(t *testing.T) {
	store := NewInMemorySuspendStore()
	ctx := context.Background()
	snapshot := runner.SuspendedRun{
		Messages:      []message.Message{message.UserText("do it")},
		LastAgentName: "test",
		LastMode:      "default",
		RunID:         "run-1",
	}

	if err := store.Save(ctx, "thread-1", snapshot); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	got, ok, err := store.Load(ctx, "thread-1")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if !ok {
		t.Fatal("Load ok = false, want true")
	}
	if got.RunID != "run-1" || got.LastAgentName != "test" {
		t.Fatalf("loaded snapshot = %+v", got)
	}
}

func TestInMemorySuspendStoreLoadMissing(t *testing.T) {
	store := NewInMemorySuspendStore()
	_, ok, err := store.Load(context.Background(), "absent")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if ok {
		t.Fatal("Load ok = true for absent key, want false")
	}
}

func TestInMemorySuspendStoreDelete(t *testing.T) {
	store := NewInMemorySuspendStore()
	ctx := context.Background()
	if err := store.Save(ctx, "thread-1", runner.SuspendedRun{RunID: "run-1"}); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if err := store.Delete(ctx, "thread-1"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, ok, _ := store.Load(ctx, "thread-1"); ok {
		t.Fatal("Load ok = true after Delete, want false")
	}
}

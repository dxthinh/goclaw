package pg

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestPGRunTimelineStoreAppendAndListBySeq(t *testing.T) {
	db := hooksTestDB(t)
	timeline := NewPGRunTimelineStore(db)
	tenantID, agentID := seedTenantAndAgent(t, db)
	ctx := store.WithTenantID(context.Background(), tenantID)

	items := []store.RunTimelineItem{
		{
			RunID:      "pg-run-1",
			SessionKey: "agent:default:direct:user-1",
			AgentID:    &agentID,
			UserID:     "user-1",
			Channel:    "web",
			Seq:        2,
			ItemType:   store.RunTimelineItemTypeToolCall,
			Status:     store.RunTimelineStatusRunning,
			Title:      "exec_command",
			Preview:    `{"cmd":"date"}`,
			ToolName:   "exec_command",
			ToolCallID: "call-1",
			Metadata:   json.RawMessage(`{"safe":true}`),
		},
		{
			RunID:      "pg-run-1",
			SessionKey: "agent:default:direct:user-1",
			UserID:     "user-1",
			Channel:    "web",
			Seq:        1,
			ItemType:   store.RunTimelineItemTypeRunStatus,
			Status:     store.RunTimelineStatusStarted,
			Title:      "Run started",
			Preview:    "hello",
		},
	}
	for i := range items {
		if err := timeline.AppendRunTimelineItem(ctx, &items[i]); err != nil {
			t.Fatalf("AppendRunTimelineItem(%d): %v", i, err)
		}
	}

	got, err := timeline.ListRunTimelineItems(ctx, store.RunTimelineListOpts{RunID: "pg-run-1", Limit: 10})
	if err != nil {
		t.Fatalf("ListRunTimelineItems: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("seq order = [%d,%d], want [1,2]", got[0].Seq, got[1].Seq)
	}
	if got[1].Content != "" {
		t.Fatalf("Content persisted = %q, want empty preview-only archive", got[1].Content)
	}
}

func TestPGRunTimelineStoreTenantScope(t *testing.T) {
	db := hooksTestDB(t)
	timeline := NewPGRunTimelineStore(db)
	tenantID, _ := seedTenantAndAgent(t, db)
	ctxA := store.WithTenantID(context.Background(), tenantID)
	ctxB := store.WithTenantID(context.Background(), uuid.Must(uuid.NewV7()))

	item := store.RunTimelineItem{
		RunID:      "pg-run-scope",
		SessionKey: "agent:default:direct:user-1",
		Seq:        1,
		ItemType:   store.RunTimelineItemTypeAssistantMessage,
		Status:     store.RunTimelineStatusCompleted,
		Title:      "Assistant",
		Preview:    "Visible only to tenant A",
		Content:    `{"raw":"must not persist"}`,
	}
	if err := timeline.AppendRunTimelineItem(ctxA, &item); err != nil {
		t.Fatalf("AppendRunTimelineItem: %v", err)
	}

	gotA, err := timeline.ListRunTimelineItems(ctxA, store.RunTimelineListOpts{RunID: "pg-run-scope"})
	if err != nil {
		t.Fatalf("List tenant A: %v", err)
	}
	if len(gotA) != 1 {
		t.Fatalf("tenant A len = %d, want 1", len(gotA))
	}
	if gotA[0].Content != "" {
		t.Fatalf("Content = %q, want empty", gotA[0].Content)
	}

	gotB, err := timeline.ListRunTimelineItems(ctxB, store.RunTimelineListOpts{RunID: "pg-run-scope"})
	if err != nil {
		t.Fatalf("List tenant B: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("tenant B len = %d, want 0", len(gotB))
	}
}

func TestPGRunTimelineStoreNextSeqNeverOverwritesRunEvents(t *testing.T) {
	db := hooksTestDB(t)
	timeline := NewPGRunTimelineStore(db)
	tenantID, _ := seedTenantAndAgent(t, db)
	ctx := store.WithTenantID(context.Background(), tenantID)
	const runID = "pg-run-next-seq"
	session := "agent:default:direct:user-1"

	first := &store.RunTimelineItem{RunID: runID, SessionKey: session, ItemType: store.RunTimelineItemTypeDelivery, Title: "a.png"}
	if err := timeline.AppendRunTimelineItemNextSeq(ctx, first); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	second := &store.RunTimelineItem{RunID: runID, SessionKey: session, ItemType: store.RunTimelineItemTypeDelivery, Title: "b.png"}
	if err := timeline.AppendRunTimelineItemNextSeq(ctx, second); err != nil {
		t.Fatalf("second delivery: %v", err)
	}
	if first.Seq != store.RunTimelineDeliverySeqBase || second.Seq != store.RunTimelineDeliverySeqBase+1 {
		t.Fatalf("seqs = %d, %d", first.Seq, second.Seq)
	}

	// run.completed lands late, numbered by the recorder's in-memory counter.
	late := &store.RunTimelineItem{RunID: runID, SessionKey: session, Seq: 7, ItemType: store.RunTimelineItemTypeRunStatus, Title: "Run completed"}
	if err := timeline.AppendRunTimelineItem(ctx, late); err != nil {
		t.Fatalf("late run event: %v", err)
	}

	got, err := timeline.ListRunTimelineItems(ctx, store.RunTimelineListOpts{RunID: runID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 || got[0].Title != "Run completed" || got[1].Title != "a.png" || got[2].Title != "b.png" {
		t.Fatalf("items = %+v", got)
	}
}

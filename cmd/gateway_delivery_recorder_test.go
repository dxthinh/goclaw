package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type fakeDeliveryTimeline struct {
	store.RunTimelineStore
	items []store.RunTimelineItem
}

func (f *fakeDeliveryTimeline) AppendRunTimelineItemNextSeq(_ context.Context, item *store.RunTimelineItem) error {
	f.items = append(f.items, *item)
	return nil
}

type fakeDeliverySpans struct{ spans []store.SpanData }

func (f *fakeDeliverySpans) EmitSpan(span store.SpanData) { f.spans = append(f.spans, span) }

type fakeDeliverySessions struct {
	notes []providers.Message
	saved int
}

func (f *fakeDeliverySessions) AddMessage(_ context.Context, _ string, msg providers.Message) {
	f.notes = append(f.notes, msg)
}

func (f *fakeDeliverySessions) Save(context.Context, string) error {
	f.saved++
	return nil
}

func deliveryFixture() (*deliveryRecorder, *fakeDeliveryTimeline, *fakeDeliverySpans, *fakeDeliverySessions) {
	timeline, spans, sessions := &fakeDeliveryTimeline{}, &fakeDeliverySpans{}, &fakeDeliverySessions{}
	return &deliveryRecorder{timeline: timeline, spans: spans, sessions: sessions}, timeline, spans, sessions
}

func deliveredMsg(traceID uuid.UUID) bus.OutboundMessage {
	return bus.OutboundMessage{
		Channel:  "zp",
		ChatID:   "c1",
		TenantID: uuid.New(),
		Metadata: map[string]string{
			bus.MetaDeliveryRunID:      "run-1",
			bus.MetaDeliverySessionKey: "agent:lead:zp:group:c1",
			bus.MetaDeliveryTraceID:    traceID.String(),
		},
	}
}

func TestDeliveryRecorder_SuccessWritesTimelineAndSpanOnly(t *testing.T) {
	r, timeline, spans, sessions := deliveryFixture()
	traceID := uuid.New()

	r.record(channels.DeliveryOutcome{
		Msg:      deliveredMsg(traceID),
		Media:    bus.MediaAttachment{URL: "/ws/final.png", ContentType: "image/png"},
		Attempts: 2,
	})

	if len(timeline.items) != 1 {
		t.Fatalf("timeline items = %d, want 1", len(timeline.items))
	}
	item := timeline.items[0]
	if item.ItemType != store.RunTimelineItemTypeDelivery || item.Status != store.RunTimelineStatusCompleted ||
		item.Title != "final.png" || item.RunID != "run-1" || item.TraceID == nil || *item.TraceID != traceID {
		t.Fatalf("item = %+v", item)
	}
	var meta map[string]any
	if err := json.Unmarshal(item.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["path"] != "/ws/final.png" || meta["attempts"] != float64(2) || meta["error"] != nil {
		t.Fatalf("metadata = %v", meta)
	}
	if len(spans.spans) != 1 || spans.spans[0].Status != store.SpanStatusCompleted || spans.spans[0].TraceID != traceID {
		t.Fatalf("spans = %+v", spans.spans)
	}
	if len(sessions.notes) != 0 {
		t.Fatalf("a delivered file must not leave a session note: %+v", sessions.notes)
	}
}

func TestDeliveryRecorder_FailureRecordsErrorAndSessionNote(t *testing.T) {
	r, timeline, spans, sessions := deliveryFixture()

	r.record(channels.DeliveryOutcome{
		Msg:      deliveredMsg(uuid.New()),
		Media:    bus.MediaAttachment{URL: "/ws/final.zip"},
		Attempts: 1,
		Err:      errors.New("inner error code 201: chunk over 512K"),
	})

	if timeline.items[0].Status != store.RunTimelineStatusFailed || timeline.items[0].Preview == "" {
		t.Fatalf("item = %+v", timeline.items[0])
	}
	if spans.spans[0].Status != store.SpanStatusError || spans.spans[0].Error == "" {
		t.Fatalf("span = %+v", spans.spans[0])
	}
	if len(sessions.notes) != 1 || sessions.saved != 1 {
		t.Fatalf("session notes = %+v, saved = %d", sessions.notes, sessions.saved)
	}
}

func TestDeliveryRecorder_SkipsWithoutRunIdentity(t *testing.T) {
	r, timeline, spans, _ := deliveryFixture()

	r.record(channels.DeliveryOutcome{
		Msg:   bus.OutboundMessage{Channel: "zp", ChatID: "c1"},
		Media: bus.MediaAttachment{URL: "/ws/a.png"},
	})

	if len(timeline.items) != 0 || len(spans.spans) != 0 {
		t.Fatalf("recorded without a run: %+v %+v", timeline.items, spans.spans)
	}
}

func TestAttachDeliveryTrace(t *testing.T) {
	shared := map[string]string{"local_key": "lk"}
	traceID, tenantID := uuid.New(), uuid.New()

	withMedia := bus.OutboundMessage{Metadata: shared}
	appendMediaToOutbound(&withMedia, []agent.MediaResult{{Path: "/ws/a.png"}})
	attachDeliveryTrace(&withMedia, deliveryTrace{runID: "run-1", sessionKey: "s", traceID: traceID, tenantID: tenantID})
	if withMedia.Metadata[bus.MetaDeliveryRunID] != "run-1" || withMedia.Metadata["local_key"] != "lk" || withMedia.TenantID != tenantID {
		t.Fatalf("msg = %+v", withMedia)
	}
	if _, leaked := shared[bus.MetaDeliveryRunID]; leaked {
		t.Fatal("the caller's shared metadata map must not be mutated")
	}

	textOnly := bus.OutboundMessage{Metadata: shared}
	attachDeliveryTrace(&textOnly, deliveryTrace{runID: "run-1", sessionKey: "s"})
	if _, set := textOnly.Metadata[bus.MetaDeliveryRunID]; set {
		t.Fatal("text-only messages carry no delivery trace")
	}
}

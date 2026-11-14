package tasks

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// dispatchCall là một lần ticker gọi DispatchRecoveredTasks.
type dispatchCall struct {
	TeamID   uuid.UUID
	TenantID uuid.UUID
	TaskIDs  []uuid.UUID
}

// dispatchRecorder ghi lại các lần ticker gọi DispatchRecoveredTasks.
type dispatchRecorder struct {
	mu    sync.Mutex
	calls []dispatchCall
}

func (r *dispatchRecorder) DispatchRecoveredTasks(ctx context.Context, teamID uuid.UUID, taskIDs []uuid.UUID) {
	r.mu.Lock()
	r.calls = append(r.calls, dispatchCall{TeamID: teamID, TenantID: store.TenantIDFromContext(ctx), TaskIDs: taskIDs})
	r.mu.Unlock()
}

type tickerHarness struct {
	tt       *TaskTicker
	ts       *stubTeamStore
	bus      *bus.MessageBus
	recorder *dispatchRecorder
	tenantID uuid.UUID
	teamID   uuid.UUID
}

func newTickerHarness() *tickerHarness {
	tenantID, teamID, leadID := uuid.New(), uuid.New(), uuid.New()
	info := func(n int, subject string) store.RecoveredTaskInfo {
		return store.RecoveredTaskInfo{ID: uuid.New(), TeamID: teamID, TenantID: tenantID, TaskNumber: n, Subject: subject, Channel: "telegram", ChatID: "chat-1"}
	}
	ts := &stubTeamStore{
		recovered: []store.RecoveredTaskInfo{info(1, "recovered task")},
		stale:     []store.RecoveredTaskInfo{info(2, "stale task")},
		inReview:  []store.RecoveredTaskInfo{info(3, "review task")},
		orphans:   []store.RecoveredTaskInfo{info(4, "orphan task")},
		getTeamFunc: func(_ context.Context, id uuid.UUID) (*store.TeamData, error) {
			return &store.TeamData{BaseModel: store.BaseModel{ID: id}, LeadAgentID: leadID}, nil
		},
	}
	as := &stubAgentStore{getByIDFunc: func(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
		return &store.AgentData{BaseModel: store.BaseModel{ID: id}, AgentKey: "lead"}, nil
	}}
	mb := bus.New()
	rec := &dispatchRecorder{}
	return &tickerHarness{
		tt: NewTaskTicker(ts, as, mb, rec, 3600), ts: ts, bus: mb, recorder: rec,
		tenantID: tenantID, teamID: teamID,
	}
}

// leaderMessages đọc mọi tin ticker gửi cho lead, theo nhãn hành động.
func (h *tickerHarness) leaderMessages() map[string]string {
	out := map[string]string{}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		msg, ok := h.bus.ConsumeInbound(ctx)
		cancel()
		if !ok {
			return out
		}
		switch {
		case strings.Contains(msg.Content, "recovered (lock expired)"):
			out["recovered"] = msg.Content
		case strings.Contains(msg.Content, "in review too long"):
			out["in_review"] = msg.Content
		case strings.Contains(msg.Content, "marked stale"):
			out["stale"] = msg.Content
		case strings.Contains(msg.Content, "auto-unblocked"):
			out["orphan"] = msg.Content
		}
	}
}

// Task recover về pending: retry chỉ nhận stale/failed/completed nên không được gợi ý retry.
func TestTickerHints_RecoveredTasksDoNotSuggestRetry(t *testing.T) {
	h := newTickerHarness()
	h.tt.recoverAll(false)
	msg := h.leaderMessages()["recovered"]
	if msg == "" {
		t.Fatal("không có tin báo task recovered")
	}
	if strings.Contains(msg, `action="retry"`) {
		t.Errorf("tin recovered gợi ý retry cho task pending:\n%s", msg)
	}
}

// update không nhận status; hủy task phải dùng action cancel.
func TestTickerHints_CancelUsesCancelAction(t *testing.T) {
	h := newTickerHarness()
	h.tt.recoverAll(false)
	msgs := h.leaderMessages()
	for _, key := range []string{"recovered", "stale", "in_review"} {
		msg := msgs[key]
		if msg == "" {
			t.Fatalf("không có tin %s", key)
		}
		if strings.Contains(msg, `status="cancelled"`) || strings.Contains(msg, `action="update"`) {
			t.Errorf("tin %s gợi ý update status:\n%s", key, msg)
		}
		if !strings.Contains(msg, `action="cancel"`) {
			t.Errorf("tin %s không nêu action cancel:\n%s", key, msg)
		}
	}
}

// Task in_review đã thành stale; approve đòi in_review nên không được gợi ý approve.
func TestTickerHints_StaleReviewDoesNotSuggestApprove(t *testing.T) {
	h := newTickerHarness()
	h.tt.recoverAll(false)
	msg := h.leaderMessages()["in_review"]
	if msg == "" {
		t.Fatal("không có tin báo in_review stale")
	}
	if strings.Contains(msg, `action="approve"`) || strings.Contains(msg, `action="reject"`) {
		t.Errorf("tin in_review stale gợi ý approve/reject cho task stale:\n%s", msg)
	}
	if !strings.Contains(msg, `action="retry"`) {
		t.Errorf("tin in_review stale phải gợi ý retry:\n%s", msg)
	}
}

// Ticker phải dispatch lại task đã recover ngay trong nhịp đó, với context mang
// tenant, và chỉ với đúng các task nó vừa đưa về pending.
func TestTicker_RedispatchesRecoveredTasks(t *testing.T) {
	for _, force := range []bool{true, false} {
		h := newTickerHarness()
		h.tt.recoverAll(force)
		h.recorder.mu.Lock()
		calls := append([]dispatchCall(nil), h.recorder.calls...)
		h.recorder.mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("force=%v: DispatchRecoveredTasks calls = %d, want 1 (một lần mỗi team)", force, len(calls))
		}
		if calls[0].TeamID != h.teamID || calls[0].TenantID != h.tenantID {
			t.Errorf("force=%v: dispatch call = %+v, want team %s tenant %s", force, calls[0], h.teamID, h.tenantID)
		}
		want := map[uuid.UUID]bool{h.ts.recovered[0].ID: true, h.ts.orphans[0].ID: true}
		if len(calls[0].TaskIDs) != len(want) {
			t.Fatalf("force=%v: task IDs = %v, want recovered + orphan", force, calls[0].TaskIDs)
		}
		for _, id := range calls[0].TaskIDs {
			if !want[id] {
				t.Errorf("force=%v: dispatch task %s không do ticker recover", force, id)
			}
		}
	}
}

func TestTicker_NoRecoveryNoDispatch(t *testing.T) {
	h := newTickerHarness()
	h.ts.recovered = nil
	h.ts.orphans = nil
	h.tt.recoverAll(false)
	h.recorder.mu.Lock()
	defer h.recorder.mu.Unlock()
	if len(h.recorder.calls) != 0 {
		t.Errorf("DispatchUnblockedTasks calls = %d, want 0", len(h.recorder.calls))
	}
}

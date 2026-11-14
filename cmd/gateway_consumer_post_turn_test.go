package cmd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// outcomeTeamStore ghi lại lời gọi vòng đời mà resolveTeamTaskOutcome thực hiện.
type outcomeTeamStore struct {
	store.TeamStore
	mu       sync.Mutex
	task     *store.TeamTaskData
	tasks    []store.TeamTaskData
	calls    []string
	result   string
	renewErr []error // lỗi trả về theo thứ tự cho RenewTaskLock
	renews   atomic.Int32
}

func (s *outcomeTeamStore) record(c string) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
}

func (s *outcomeTeamStore) GetTeam(_ context.Context, id uuid.UUID) (*store.TeamData, error) {
	return &store.TeamData{BaseModel: store.BaseModel{ID: id}}, nil
}

func (s *outcomeTeamStore) GetTask(_ context.Context, _ uuid.UUID) (*store.TeamTaskData, error) {
	if s.task == nil {
		return nil, errors.New("not found")
	}
	cp := *s.task
	return &cp, nil
}

func (s *outcomeTeamStore) ListTasks(_ context.Context, _ uuid.UUID, _, _, _, _, _ string, _, _ int) ([]store.TeamTaskData, error) {
	return s.tasks, nil
}

func (s *outcomeTeamStore) FailTask(_ context.Context, _, _ uuid.UUID, msg string) error {
	s.record("FailTask")
	s.result = msg
	return nil
}

func (s *outcomeTeamStore) CompleteTask(_ context.Context, _, _ uuid.UUID, result string) error {
	s.record("CompleteTask")
	s.result = result
	return nil
}

func (s *outcomeTeamStore) RenewTaskLock(_ context.Context, _, _ uuid.UUID) error {
	n := int(s.renews.Add(1))
	s.record("RenewTaskLock")
	if n <= len(s.renewErr) {
		return s.renewErr[n-1]
	}
	return nil
}

type outcomePostTurn struct{ dispatched atomic.Int32 }

func (p *outcomePostTurn) ProcessPendingTasks(context.Context, uuid.UUID, []uuid.UUID) error {
	return nil
}
func (p *outcomePostTurn) DispatchUnblockedTasks(context.Context, uuid.UUID) { p.dispatched.Add(1) }

type outcomeCase struct {
	ts     *outcomeTeamStore
	pt     *outcomePostTurn
	deps   *ConsumerDeps
	events []string
	meta   teammateTaskMeta
	note   string
}

func newOutcomeCase(status string) *outcomeCase {
	c := &outcomeCase{
		ts: &outcomeTeamStore{task: &store.TeamTaskData{Status: status, Subject: "s", TaskNumber: 1}},
		pt: &outcomePostTurn{},
	}
	mb := bus.New()
	mb.Subscribe("test", func(e bus.Event) { c.events = append(c.events, e.Name) })
	c.deps = &ConsumerDeps{TeamStore: c.ts, PostTurn: c.pt, MsgBus: mb}
	c.meta = teammateTaskMeta{TaskID: uuid.New(), TeamID: uuid.New(), ToAgent: "member"}
	return c
}

func (c *outcomeCase) run(outcome scheduler.RunOutcome, flags tools.TaskActionFlags) {
	_, c.note = resolveTeamTaskOutcome(context.Background(), c.deps, outcome, &flags, c.meta)
}

func (c *outcomeCase) assert(t *testing.T, wantCalls []string, wantEvent string) {
	t.Helper()
	if len(c.ts.calls) != len(wantCalls) {
		t.Fatalf("calls = %v, want %v", c.ts.calls, wantCalls)
	}
	for i := range wantCalls {
		if c.ts.calls[i] != wantCalls[i] {
			t.Fatalf("calls = %v, want %v", c.ts.calls, wantCalls)
		}
	}
	if wantEvent == "" && len(c.events) != 0 {
		t.Errorf("events = %v, want none", c.events)
	}
	if wantEvent != "" && (len(c.events) != 1 || c.events[0] != wantEvent) {
		t.Errorf("events = %v, want [%s]", c.events, wantEvent)
	}
	if c.pt.dispatched.Load() != 1 {
		t.Errorf("DispatchUnblockedTasks calls = %d, want 1", c.pt.dispatched.Load())
	}
}

// Đặc tả từng nhánh của resolveTeamTaskOutcome.
func TestTeamTaskOutcome_AlreadyTerminalOnlyDispatches(t *testing.T) {
	for _, st := range []string{store.TeamTaskStatusCompleted, store.TeamTaskStatusFailed, store.TeamTaskStatusCancelled} {
		c := newOutcomeCase(st)
		c.run(scheduler.RunOutcome{Err: errors.New("boom")}, tools.TaskActionFlags{})
		c.assert(t, nil, "")
	}
}

func TestTeamTaskOutcome_RunErrorFailsTask(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.run(scheduler.RunOutcome{Err: errors.New("provider down")}, tools.TaskActionFlags{})
	c.assert(t, []string{"FailTask"}, protocol.EventTeamTaskFailed)
	if c.ts.result != "provider down" {
		t.Errorf("fail reason = %q", c.ts.result)
	}
}

func TestTeamTaskOutcome_StopReasonFailsTask(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "notice", StopReason: "budget"}}, tools.TaskActionFlags{})
	c.assert(t, []string{"FailTask"}, protocol.EventTeamTaskFailed)
}

func TestTeamTaskOutcome_ToolHandledSkips(t *testing.T) {
	for _, flags := range []tools.TaskActionFlags{{Completed: true}, {Escalated: true}} {
		c := newOutcomeCase(store.TeamTaskStatusInProgress)
		c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "x"}}, flags)
		c.assert(t, nil, "")
	}
}

func TestTeamTaskOutcome_ReviewedRenewsLock(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInReview)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "x"}}, tools.TaskActionFlags{Reviewed: true})
	c.assert(t, []string{"RenewTaskLock"}, "")
}

func TestTeamTaskOutcome_LoopKilledFailsTask(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{LoopKilled: true}}, tools.TaskActionFlags{})
	c.assert(t, []string{"FailTask"}, protocol.EventTeamTaskFailed)
	if c.ts.result != "Agent run terminated by loop detector" {
		t.Errorf("fail reason = %q", c.ts.result)
	}
}

func TestTeamTaskOutcome_DefaultAutoCompletes(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "answer", Deliverables: []string{"a", "b"}}}, tools.TaskActionFlags{Progressed: true})
	c.assert(t, []string{"CompleteTask"}, protocol.EventTeamTaskCompleted)
	if c.ts.result != "a\n\n---\n\nb" {
		t.Errorf("result = %q", c.ts.result)
	}
}

func TestTeamTaskOutcome_DefaultEmptyResult(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{}}, tools.TaskActionFlags{})
	c.assert(t, []string{"CompleteTask"}, protocol.EventTeamTaskCompleted)
	if c.ts.result != "Agent run ended without explicit result" {
		t.Errorf("result = %q", c.ts.result)
	}
}

// Một lần gia hạn lỗi không được dừng hẳn vòng gia hạn.
func TestLockRenewal_ContinuesAfterError(t *testing.T) {
	old := taskLockRenewalInterval
	taskLockRenewalInterval = 10 * time.Millisecond
	t.Cleanup(func() { taskLockRenewalInterval = old })

	ts := &outcomeTeamStore{renewErr: []error{errors.New("db blip")}}
	stop := startTaskLockRenewal(context.Background(), ts, uuid.New(), uuid.New())
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ts.renews.Load() >= 3 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("RenewTaskLock calls = %d, want >= 3 (vòng gia hạn dừng sau lỗi đầu)", ts.renews.Load())
}

func TestLockRenewal_StopsOnStop(t *testing.T) {
	old := taskLockRenewalInterval
	taskLockRenewalInterval = 5 * time.Millisecond
	t.Cleanup(func() { taskLockRenewalInterval = old })

	ts := &outcomeTeamStore{}
	stop := startTaskLockRenewal(context.Background(), ts, uuid.New(), uuid.New())
	time.Sleep(30 * time.Millisecond)
	stop()
	time.Sleep(15 * time.Millisecond)
	n := ts.renews.Load()
	time.Sleep(30 * time.Millisecond)
	if ts.renews.Load() != n {
		t.Errorf("vẫn gia hạn sau khi stop: %d -> %d", n, ts.renews.Load())
	}
}

// Lượt chạy lỗi làm task failed: announce cho lead phải nêu task còn bị chặn.
func TestTeamTaskOutcome_FailureNotesBlockedDependents(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.ts.tasks = []store.TeamTaskData{
		{BaseModel: store.BaseModel{ID: uuid.New()}, TaskNumber: 9, Subject: "needs upstream", Status: store.TeamTaskStatusBlocked, BlockedBy: []uuid.UUID{c.meta.TaskID}},
		{BaseModel: store.BaseModel{ID: uuid.New()}, TaskNumber: 10, Subject: "unrelated", Status: store.TeamTaskStatusBlocked, BlockedBy: []uuid.UUID{uuid.New()}},
	}
	c.run(scheduler.RunOutcome{Err: errors.New("boom")}, tools.TaskActionFlags{})
	if !strings.Contains(c.note, "#9") || !strings.Contains(c.note, "needs upstream") {
		t.Errorf("note thiếu task bị chặn: %q", c.note)
	}
	if strings.Contains(c.note, "unrelated") {
		t.Errorf("note nêu task không liên quan: %q", c.note)
	}

	ok := newOutcomeCase(store.TeamTaskStatusInProgress)
	ok.ts.tasks = c.ts.tasks
	ok.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "done"}}, tools.TaskActionFlags{})
	if ok.note != "" {
		t.Errorf("task hoàn thành không được có note: %q", ok.note)
	}
}

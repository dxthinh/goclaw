package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

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
	settings json.RawMessage // settings của team (ví dụ bật task_contract)
	patch    map[string]any  // metadataPatch của lần SubmitTaskResultForReview gần nhất
}

func (s *outcomeTeamStore) record(c string) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
}

func (s *outcomeTeamStore) GetTeam(_ context.Context, id uuid.UUID) (*store.TeamData, error) {
	return &store.TeamData{BaseModel: store.BaseModel{ID: id}, Settings: s.settings}, nil
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

func (s *outcomeTeamStore) SubmitTaskResultForReview(_ context.Context, _, _ uuid.UUID, result string, patch map[string]any) error {
	s.record("SubmitTaskResultForReview")
	s.result = result
	s.patch = patch
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
	ts         *outcomeTeamStore
	pt         *outcomePostTurn
	deps       *ConsumerDeps
	events     []string
	meta       teammateTaskMeta
	note       string
	leadNotice string
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
	_, c.note, c.leadNotice = resolveTeamTaskOutcome(context.Background(), c.deps, outcome, &flags, c.meta)
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
	// AwaitingInput: member đã clarify, task chờ câu trả lời — không tự hoàn thành.
	for _, flags := range []tools.TaskActionFlags{{Completed: true}, {Escalated: true}, {AwaitingInput: true}} {
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

// Tự hoàn thành sau lượt chạy đi qua cổng duyệt của user: task có checkpoint
// "result" (trong contract hoặc metadata.checkpoint) không thành completed.
func TestTeamTaskOutcome_CheckpointSubmitsForUserApproval(t *testing.T) {
	contract, err := tools.NewTaskContractFromInput(map[string]any{
		"objective": "o", "deliverables": []any{"d"}, "acceptance": []any{"a"}, "checkpoint": "result",
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]any{
		"contract":    {tools.TaskMetaContract: contract.ToMetadata()},
		"no contract": {tools.TaskMetaCheckpoint: tools.TaskContractCheckpointResult},
	}
	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			c := newOutcomeCase(store.TeamTaskStatusInProgress)
			c.ts.task.Metadata = meta
			fb := &fakeTaskFeedback{}
			c.deps.TaskFeedback = fb
			c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "draft"}}, tools.TaskActionFlags{})
			c.assert(t, nil, "")
			if len(fb.submitted) != 1 {
				t.Fatalf("submitted = %v, want the task submitted for approval", fb.submitted)
			}
		})
	}
	// Không có checkpoint: vẫn tự hoàn thành như cũ.
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	fb := &fakeTaskFeedback{}
	c.deps.TaskFeedback = fb
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "done"}}, tools.TaskActionFlags{})
	c.assert(t, []string{"CompleteTask"}, protocol.EventTeamTaskCompleted)
	if len(fb.submitted) != 0 {
		t.Errorf("task without checkpoint must not be submitted: %v", fb.submitted)
	}
}

// contractTaskMeta trả metadata của task có contract hợp lệ (hai tiêu chí).
func contractTaskMeta(t *testing.T) map[string]any {
	t.Helper()
	c, err := tools.NewTaskContractFromInput(map[string]any{
		"objective": "o", "deliverables": []any{"d"}, "acceptance": []any{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{tools.TaskMetaContract: c.ToMetadata()}
}

// Team bật contract, task có contract, lượt chạy kết thúc không gọi complete:
// task chờ lead xem (in_review, checkpoint_pending = "lead"), không completed,
// và lead được báo bằng dòng đầu của announce.
func TestTeamTaskOutcome_ContractWithoutCompleteWaitsForLead(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.ts.settings = json.RawMessage(`{"task_contract":{"enabled":true}}`)
	c.ts.task.Metadata = contractTaskMeta(t)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "draft"}}, tools.TaskActionFlags{Progressed: true})
	c.assert(t, []string{"SubmitTaskResultForReview"}, protocol.EventTeamTaskReviewed)
	if c.ts.result != "draft" || c.ts.patch[tools.TaskMetaCheckpointPending] != tools.CheckpointPendingLead {
		t.Errorf("result=%q patch=%v, want draft waiting for lead", c.ts.result, c.ts.patch)
	}
	if !strings.Contains(c.leadNotice, "waits for your review") || !strings.Contains(c.leadNotice, "without calling complete") {
		t.Errorf("leadNotice = %q", c.leadNotice)
	}
}

// Team chưa bật contract: task có contract vẫn tự hoàn thành như cũ.
func TestTeamTaskOutcome_ContractDisabledTeamAutoCompletes(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.ts.task.Metadata = contractTaskMeta(t)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "draft"}}, tools.TaskActionFlags{})
	c.assert(t, []string{"CompleteTask"}, protocol.EventTeamTaskCompleted)
	if c.leadNotice != "" {
		t.Errorf("leadNotice = %q, want empty", c.leadNotice)
	}
}

// complete có tiêu chí chưa đạt đã đưa task về chờ lead: post-turn không chạm
// task nhưng announce vẫn mở đầu bằng dòng báo lead.
func TestTeamTaskOutcome_ToolLeadReviewAddsNotice(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInReview)
	c.ts.settings = json.RawMessage(`{"task_contract":{"enabled":true}}`)
	c.ts.task.Metadata = contractTaskMeta(t)
	c.ts.task.Metadata[tools.TaskMetaCheckpointPending] = tools.CheckpointPendingLead
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "partial"}}, tools.TaskActionFlags{Completed: true})
	c.assert(t, nil, "")
	if !strings.Contains(c.leadNotice, "not every acceptance criterion is met") {
		t.Errorf("leadNotice = %q", c.leadNotice)
	}
}

// Kết quả dài hơn giới hạn được cắt theo ký tự và vẫn là UTF-8 hợp lệ.
func TestTruncateTaskResult_RuneSafe(t *testing.T) {
	long := strings.Repeat("ệ", taskResultMaxRunes+10)
	got := truncateTaskResult(long)
	if !utf8.ValidString(got) {
		t.Fatal("truncated result is not valid UTF-8")
	}
	if want := strings.Repeat("ệ", taskResultMaxRunes) + "\n[truncated]"; got != want {
		t.Errorf("truncated length = %d runes", utf8.RuneCountInString(got))
	}
	if short := "kết quả"; truncateTaskResult(short) != short {
		t.Error("short result must be unchanged")
	}
	// Qua resolveTeamTaskOutcome: result ghi xuống store là UTF-8 hợp lệ.
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: long}}, tools.TaskActionFlags{})
	if !utf8.ValidString(c.ts.result) || !strings.HasSuffix(c.ts.result, "[truncated]") {
		t.Error("auto-complete result must be truncated by rune")
	}
}

// Trả lời của member bị nén (rỗng hoặc NO_REPLY, ok = false): có leadNotice thì
// vẫn báo lead với nội dung chỉ gồm notice; không có thì bỏ như cũ.
func TestComposeLeadAnnounce(t *testing.T) {
	if got, ok := composeLeadAnnounce("", false, "", "[Task #1 waits for your review]"); !ok || got != "[Task #1 waits for your review]" {
		t.Errorf("suppressed reply with notice = %q, %v; want the notice alone", got, ok)
	}
	if _, ok := composeLeadAnnounce("", false, "", ""); ok {
		t.Error("suppressed reply without notice must stay suppressed")
	}
	if got, ok := composeLeadAnnounce("result", true, "\nfail", "notice"); !ok || got != "notice\n\nresult\nfail" {
		t.Errorf("normal announce = %q", got)
	}
	if got, _ := composeLeadAnnounce("result", true, "", ""); got != "result" {
		t.Errorf("plain announce = %q", got)
	}
}

// GetTask lỗi sau lượt chạy: không biết task có contract hay checkpoint, nên
// không tự hoàn thành (task giữ in_progress để ticker xử lý).
func TestTeamTaskOutcome_TaskLookupErrorDoesNotComplete(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInProgress)
	c.ts.task = nil
	c.run(scheduler.RunOutcome{Result: &agent.RunResult{Content: "draft"}}, tools.TaskActionFlags{})
	c.assert(t, nil, "")
}

// complete đưa task về chờ lead rồi vòng sau của lượt chạy lỗi: không FailTask,
// lead vẫn nhận dòng báo chờ xem.
func TestTeamTaskOutcome_LeadReviewSurvivesLaterRunFailure(t *testing.T) {
	c := newOutcomeCase(store.TeamTaskStatusInReview)
	c.ts.settings = json.RawMessage(`{"task_contract":{"enabled":true}}`)
	c.ts.task.Metadata = contractTaskMeta(t)
	c.ts.task.Metadata[tools.TaskMetaCheckpointPending] = tools.CheckpointPendingLead
	c.run(scheduler.RunOutcome{Err: errors.New("provider timeout")}, tools.TaskActionFlags{Completed: true})
	c.assert(t, nil, "")
	if !strings.Contains(c.leadNotice, "waits for your review") {
		t.Errorf("leadNotice = %q, want the review notice", c.leadNotice)
	}
}

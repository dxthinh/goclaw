package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// dispatchAgentStore chỉ cài GetByID — dispatch chỉ cần tra agent theo ID.
type dispatchAgentStore struct {
	store.AgentStore
	agents map[uuid.UUID]*store.AgentData
}

func (s *dispatchAgentStore) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	if ag, ok := s.agents[id]; ok {
		return ag, nil
	}
	return nil, fmt.Errorf("agent not found: %s", id)
}

// dispatchHarness gom TeamToolManager thật với store giả và bus thật để
// quan sát event phát ra và tin nhắn inbound.
type dispatchHarness struct {
	mgr    *TeamToolManager
	tasks  *mockTaskStore
	bus    *bus.MessageBus
	team   *store.TeamData
	mu     sync.Mutex
	events []bus.Event
}

func newDispatchHarness(t *testing.T) *dispatchHarness {
	t.Helper()
	team := &store.TeamData{
		BaseModel:   store.BaseModel{ID: testTeamID},
		Name:        "test-team",
		LeadAgentID: testLeadID,
		Status:      store.TeamStatusActive,
	}
	agents := &dispatchAgentStore{agents: map[uuid.UUID]*store.AgentData{
		testLeadID:   {BaseModel: store.BaseModel{ID: testLeadID}, AgentKey: "lead-agent", DisplayName: "Lead"},
		testMemberID: {BaseModel: store.BaseModel{ID: testMemberID}, AgentKey: "member-agent", DisplayName: "Member"},
	}}
	h := &dispatchHarness{
		tasks: newMockTaskStore(team, nil),
		bus:   bus.New(),
		team:  team,
	}
	h.bus.Subscribe("test", func(e bus.Event) {
		h.mu.Lock()
		h.events = append(h.events, e)
		h.mu.Unlock()
	})
	h.mgr = NewTeamToolManager(h.tasks, agents, h.bus, t.TempDir())
	return h
}

func (h *dispatchHarness) ctx() context.Context {
	return store.WithTenantID(context.Background(), testTenantID)
}

func (h *dispatchHarness) addTask(task *store.TeamTaskData) *store.TeamTaskData {
	if task.ID == uuid.Nil {
		task.ID = uuid.New()
	}
	task.TeamID = testTeamID
	if task.Channel == "" {
		task.Channel = "telegram"
		task.ChatID = "chat-1"
	}
	cp := *task
	h.tasks.tasks[task.ID] = &cp
	return task
}

func (h *dispatchHarness) eventNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e.Name)
	}
	return out
}

func (h *dispatchHarness) hasEvent(name string) bool {
	return slices.Contains(h.eventNames(), name)
}

// drainInbound đọc mọi tin đang nằm trong hàng đợi inbound.
func (h *dispatchHarness) drainInbound() []bus.InboundMessage {
	var out []bus.InboundMessage
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		msg, ok := h.bus.ConsumeInbound(ctx)
		cancel()
		if !ok {
			return out
		}
		out = append(out, msg)
	}
}

func (h *dispatchHarness) leadMessages() []bus.InboundMessage {
	var out []bus.InboundMessage
	for _, m := range h.drainInbound() {
		if m.AgentID == "lead-agent" {
			out = append(out, m)
		}
	}
	return out
}

func (h *dispatchHarness) status(id uuid.UUID) string {
	t, _ := h.tasks.GetTask(context.Background(), id)
	return t.Status
}

// assertVisibleFailure kiểm tra ba dấu hiệu của một dispatch thất bại: task
// thành failed, có event failed, và lead nhận được tin báo.
func (h *dispatchHarness) assertVisibleFailure(t *testing.T, taskID uuid.UUID) {
	t.Helper()
	if got := h.status(taskID); got != store.TeamTaskStatusFailed {
		t.Errorf("task status = %q, want failed", got)
	}
	if !h.hasEvent(protocol.EventTeamTaskFailed) {
		t.Errorf("không có event %s; events = %v", protocol.EventTeamTaskFailed, h.eventNames())
	}
	if msgs := h.leadMessages(); len(msgs) == 0 {
		t.Error("lead không nhận được tin báo dispatch thất bại")
	}
}

func TestDispatchFailure_LeadGuardIsVisible(t *testing.T) {
	h := newDispatchHarness(t)
	lead := testLeadID
	task := h.addTask(&store.TeamTaskData{Subject: "lead task", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &lead})

	h.mgr.dispatchTaskToAgent(h.ctx(), task, h.team, testLeadID)

	h.assertVisibleFailure(t, task.ID)
}

func TestDispatchFailure_CircuitBreakerIsVisible(t *testing.T) {
	h := newDispatchHarness(t)
	member := testMemberID
	task := h.addTask(&store.TeamTaskData{
		Subject: "looping task", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &member,
		Metadata: map[string]any{"dispatch_count": float64(maxTaskDispatches)},
	})

	h.mgr.dispatchTaskToAgent(h.ctx(), task, h.team, testMemberID)

	h.assertVisibleFailure(t, task.ID)
}

func TestDispatchFailure_UnresolvableAgentIsVisible(t *testing.T) {
	h := newDispatchHarness(t)
	ghost := uuid.New()
	task := h.addTask(&store.TeamTaskData{Subject: "ghost task", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &ghost})

	h.mgr.dispatchTaskToAgent(h.ctx(), task, h.team, ghost)

	h.assertVisibleFailure(t, task.ID)
}

func TestDispatchFailure_FullInboundQueueIsVisible(t *testing.T) {
	h := newDispatchHarness(t)
	member := testMemberID
	task := h.addTask(&store.TeamTaskData{Subject: "dropped task", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &member})
	for h.bus.TryPublishInbound(bus.InboundMessage{Content: "filler"}) {
	}

	h.mgr.dispatchTaskToAgent(h.ctx(), task, h.team, testMemberID)

	// Hàng đợi đầy nên lead không nhận được tin; task vẫn phải failed và có event.
	if got := h.status(task.ID); got != store.TeamTaskStatusFailed {
		t.Errorf("task status = %q, want failed", got)
	}
	if !h.hasEvent(protocol.EventTeamTaskFailed) {
		t.Errorf("không có event %s; events = %v", protocol.EventTeamTaskFailed, h.eventNames())
	}
}

func TestDispatchFailure_LeadOwnedPendingTaskIsVisible(t *testing.T) {
	h := newDispatchHarness(t)
	lead := testLeadID
	task := h.addTask(&store.TeamTaskData{Subject: "lead pending", Status: store.TeamTaskStatusPending, OwnerAgentID: &lead})

	h.mgr.DispatchUnblockedTasks(h.ctx(), testTeamID)

	h.assertVisibleFailure(t, task.ID)
}

// Đặc tả nội dung tin dispatch: giữ nguyên các phần member đang dựa vào.
func TestDispatchMessageContent(t *testing.T) {
	h := newDispatchHarness(t)
	member := testMemberID
	task := h.addTask(&store.TeamTaskData{
		Subject: "write report", Description: "details here",
		Status: store.TeamTaskStatusInProgress, OwnerAgentID: &member,
		Metadata: map[string]any{
			"team_workspace": "/ws/team",
			"attached_files": []any{"/ws/team/attachments/brief.pdf"},
		},
	})

	h.mgr.dispatchTaskToAgent(h.ctx(), task, h.team, testMemberID)

	msgs := h.drainInbound()
	if len(msgs) != 1 {
		t.Fatalf("inbound = %d, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Channel != "system" || msg.SenderID != "teammate:dashboard" || msg.AgentID != "member-agent" {
		t.Errorf("routing = %q/%q/%q", msg.Channel, msg.SenderID, msg.AgentID)
	}
	for _, want := range []string{
		fmt.Sprintf("[Assigned task #%d (id: %s)]: write report", task.TaskNumber, task.ID),
		"\n\ndetails here",
		"[Team workspace: /ws/team",
		"- attachments/brief.pdf",
		`team_tasks(action="progress"`,
		`team_tasks(action="comment", type="blocker"`,
		`team_tasks(action="complete", result=`,
	} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("content thiếu %q:\n%s", want, msg.Content)
		}
	}
	if msg.Metadata[MetaTeamTaskID] != task.ID.String() || msg.Metadata[MetaToAgent] != "member-agent" ||
		msg.Metadata[MetaFromAgent] != "lead-agent" || msg.Metadata[MetaOriginChannel] != "telegram" {
		t.Errorf("metadata = %v", msg.Metadata)
	}
	got, _ := h.tasks.GetTask(context.Background(), task.ID)
	if dc, _ := got.Metadata["dispatch_count"].(int); dc != 1 {
		t.Errorf("dispatch_count = %v, want 1", got.Metadata["dispatch_count"])
	}
}

// Owner đang chạy một task thì task pending của họ tiếp tục xếp hàng.
func TestDispatchUnblocked_SkipsOwnerWithRunningTask(t *testing.T) {
	h := newDispatchHarness(t)
	member := testMemberID
	h.addTask(&store.TeamTaskData{Subject: "running", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &member})
	queued := h.addTask(&store.TeamTaskData{Subject: "queued", Status: store.TeamTaskStatusPending, OwnerAgentID: &member})

	h.mgr.DispatchUnblockedTasks(h.ctx(), testTeamID)

	if got := h.status(queued.ID); got != store.TeamTaskStatusPending {
		t.Errorf("queued status = %q, want pending (owner đang bận)", got)
	}
	if msgs := h.drainInbound(); len(msgs) != 0 {
		t.Errorf("dispatch %d tin, want 0", len(msgs))
	}
}

// DispatchRecoveredTasks chỉ dispatch đúng các task được chỉ định.
func TestDispatchRecoveredTasks_OnlyListedTasks(t *testing.T) {
	h := newDispatchHarness(t)
	member := testMemberID
	other := uuid.New()
	h.mgr.agentStore.(*dispatchAgentStore).agents[other] = &store.AgentData{BaseModel: store.BaseModel{ID: other}, AgentKey: "other-agent"}
	recovered := h.addTask(&store.TeamTaskData{Subject: "recovered", Status: store.TeamTaskStatusPending, OwnerAgentID: &member})
	fresh := h.addTask(&store.TeamTaskData{Subject: "just created", Status: store.TeamTaskStatusPending, OwnerAgentID: &other})

	h.mgr.DispatchRecoveredTasks(h.ctx(), testTeamID, []uuid.UUID{recovered.ID})

	if got := h.status(recovered.ID); got != store.TeamTaskStatusInProgress {
		t.Errorf("recovered status = %q, want in_progress", got)
	}
	if got := h.status(fresh.ID); got != store.TeamTaskStatusPending {
		t.Errorf("task không được chỉ định bị dispatch: status = %q", got)
	}
}

// Hàng đợi đầy: tin báo lead được thử lại và tới nơi khi hàng đợi có chỗ.
func TestDispatchFailure_FullQueueLeadNoticeIsRetried(t *testing.T) {
	oldInterval, oldMax := leadNoticeRetryInterval, leadNoticeMaxRetries
	leadNoticeRetryInterval, leadNoticeMaxRetries = 10*time.Millisecond, 200
	t.Cleanup(func() { leadNoticeRetryInterval, leadNoticeMaxRetries = oldInterval, oldMax })

	h := newDispatchHarness(t)
	member := testMemberID
	task := h.addTask(&store.TeamTaskData{Subject: "dropped task", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &member})
	for h.bus.TryPublishInbound(bus.InboundMessage{Content: "filler"}) {
	}

	h.mgr.dispatchTaskToAgent(h.ctx(), task, h.team, testMemberID)

	// Consumer bắt đầu đọc hàng đợi: tin báo lead phải xuất hiện.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		msg, ok := h.bus.ConsumeInbound(ctx)
		cancel()
		if ok && msg.AgentID == "lead-agent" {
			if !strings.Contains(msg.Content, task.ID.String()) {
				t.Errorf("tin báo lead không nêu task: %s", msg.Content)
			}
			return
		}
	}
	t.Fatal("lead không nhận được tin báo sau khi hàng đợi có chỗ")
}

// truncatedRecoverableStore mô phỏng ListRecoverableTasks bị giới hạn số dòng
// (team có nhiều task pending): task vừa recover không có trong danh sách trả về.
type truncatedRecoverableStore struct{ *mockTaskStore }

func (s truncatedRecoverableStore) ListRecoverableTasks(context.Context, uuid.UUID) ([]store.TeamTaskData, error) {
	return nil, nil
}

func TestDispatchRecoveredTasks_NotLimitedByRecoverableListCap(t *testing.T) {
	h := newDispatchHarness(t)
	h.mgr = NewTeamToolManager(truncatedRecoverableStore{h.tasks}, h.mgr.agentStore, h.bus, t.TempDir())
	member := testMemberID
	recovered := h.addTask(&store.TeamTaskData{Subject: "recovered", Status: store.TeamTaskStatusPending, OwnerAgentID: &member})

	h.mgr.DispatchRecoveredTasks(h.ctx(), testTeamID, []uuid.UUID{recovered.ID})

	if got := h.status(recovered.ID); got != store.TeamTaskStatusInProgress {
		t.Errorf("recovered status = %q, want in_progress", got)
	}
}

// cappedBoardStore mô phỏng bảng task có quá nhiều dòng active: danh sách
// ListTasks bị cắt và không còn chứa task in_progress của owner.
type cappedBoardStore struct{ *mockTaskStore }

func (s cappedBoardStore) ListTasks(context.Context, uuid.UUID, string, string, string, string, string, int, int) ([]store.TeamTaskData, error) {
	return nil, nil
}

func TestDispatchUnblocked_BusyOwnerNotHiddenByBoardCap(t *testing.T) {
	h := newDispatchHarness(t)
	h.mgr = NewTeamToolManager(cappedBoardStore{h.tasks}, h.mgr.agentStore, h.bus, t.TempDir())
	member := testMemberID
	h.addTask(&store.TeamTaskData{Subject: "running", Status: store.TeamTaskStatusInProgress, OwnerAgentID: &member})
	queued := h.addTask(&store.TeamTaskData{Subject: "queued", Status: store.TeamTaskStatusPending, OwnerAgentID: &member})

	h.mgr.DispatchUnblockedTasks(h.ctx(), testTeamID)

	if got := h.status(queued.ID); got != store.TeamTaskStatusPending {
		t.Errorf("queued status = %q, want pending (owner đang bận)", got)
	}
}

// slowBusyStore làm chậm bước đọc owner bận để hai lần dispatch song song
// cùng đọc trước khi bên kia kịp assign.
type slowBusyStore struct{ *mockTaskStore }

func (s slowBusyStore) ListBusyTaskOwners(ctx context.Context, teamID uuid.UUID) ([]uuid.UUID, error) {
	owners, err := s.mockTaskStore.ListBusyTaskOwners(ctx, teamID)
	time.Sleep(30 * time.Millisecond) // kết quả đã đọc; bên kia kịp đọc cùng trạng thái
	return owners, err
}

// Hai lần dispatch cùng team chạy song song (ticker, post-turn của hai member)
// vẫn chỉ cho owner chạy một task.
func TestDispatch_ConcurrentCallsKeepOneTaskPerOwner(t *testing.T) {
	h := newDispatchHarness(t)
	h.mgr = NewTeamToolManager(slowBusyStore{h.tasks}, h.mgr.agentStore, h.bus, t.TempDir())
	member := testMemberID
	h.addTask(&store.TeamTaskData{Subject: "recovered", Status: store.TeamTaskStatusPending, OwnerAgentID: &member, Priority: 2})
	h.addTask(&store.TeamTaskData{Subject: "queued", Status: store.TeamTaskStatusPending, OwnerAgentID: &member, Priority: 1})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.mgr.DispatchUnblockedTasks(h.ctx(), testTeamID) }()
	go func() { defer wg.Done(); h.mgr.DispatchUnblockedTasks(h.ctx(), testTeamID) }()
	wg.Wait()

	running := 0
	for _, task := range h.tasks.tasks {
		if task.Status == store.TeamTaskStatusInProgress {
			running++
		}
	}
	if running != 1 {
		t.Errorf("in_progress tasks = %d, want 1 (một task mỗi owner)", running)
	}
}

// pagedListStore phân trang ListTasks giống store thật (trả tối đa limit+1 dòng
// theo thứ tự ổn định), để kiểm tra việc đọc qua nhiều trang.
type pagedListStore struct {
	*mockTaskStore
	ordered []uuid.UUID
}

func (s pagedListStore) ListTasks(_ context.Context, _ uuid.UUID, _, _, _, _, _ string, limit, offset int) ([]store.TeamTaskData, error) {
	var out []store.TeamTaskData
	for i := offset; i < len(s.ordered) && len(out) < limit+1; i++ {
		out = append(out, *s.tasks[s.ordered[i]])
	}
	return out, nil
}

func TestListBlockedDependents_ReadsAllPages(t *testing.T) {
	h := newDispatchHarness(t)
	upstream := uuid.New()
	ps := pagedListStore{mockTaskStore: h.tasks}
	var want uuid.UUID
	for i := range 450 {
		task := h.addTask(&store.TeamTaskData{Subject: fmt.Sprintf("task %d", i), Status: store.TeamTaskStatusPending})
		if i == 420 {
			want = task.ID
			h.tasks.tasks[task.ID].Status = store.TeamTaskStatusBlocked
			h.tasks.tasks[task.ID].BlockedBy = []uuid.UUID{upstream}
		}
		ps.ordered = append(ps.ordered, task.ID)
	}

	got := ListBlockedDependents(h.ctx(), ps, testTeamID, upstream)
	if len(got) != 1 || got[0].ID != want {
		t.Fatalf("dependents = %d, want task ở trang thứ ba", len(got))
	}
}

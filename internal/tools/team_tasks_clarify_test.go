package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// makeFeedbackTask tạo task của member ở chat Telegram, có hoặc không có contract.
func makeFeedbackTask(t *testing.T, mb *mockBackend, status string, withContract bool, checkpointPending string) uuid.UUID {
	t.Helper()
	owner := testMemberID
	meta := map[string]any{}
	if withContract {
		c, err := NewTaskContractFromInput(validContractArgs())
		if err != nil {
			t.Fatal(err)
		}
		meta[TaskMetaContract] = c.ToMetadata()
	}
	if checkpointPending != "" {
		meta[TaskMetaCheckpointPending] = checkpointPending
	}
	task := &store.TeamTaskData{
		TeamID: testTeamID, Subject: "Write report", Status: status, OwnerAgentID: &owner,
		Channel: "telegram", ChatID: "chat-1", TaskNumber: 7, Metadata: meta,
	}
	preloadTask(mb, task)
	return task.ID
}

func memberCtx(ctx context.Context) context.Context {
	c := store.WithAgentID(ctx, testMemberID)
	c = WithToolChannel(c, ChannelTeammate)
	return WithTaskActionFlags(c, &TaskActionFlags{})
}

func taskContract(t *testing.T, mb *mockBackend, id uuid.UUID) *TaskContract {
	t.Helper()
	c := TaskContractFromTask(readTask(mb, id))
	if c == nil {
		t.Fatal("task has no contract")
	}
	return c
}

func TestClarify_PausesTaskAndNotifiesLead(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")
	mCtx := memberCtx(ctx)

	res := tool.Execute(mCtx, map[string]any{"action": "clarify", "task_id": taskID.String(), "text": "Which quarter?"})
	if res.IsError {
		t.Fatalf("clarify failed: %s", res.ForLLM)
	}
	task := readTask(mb, taskID)
	if task.Status != store.TeamTaskStatusAwaitingInput {
		t.Fatalf("status = %q, want awaiting_input", task.Status)
	}
	if task.FollowupAt == nil || task.FollowupChannel != "telegram" {
		t.Errorf("follow-up not set on origin chat: %+v", task.FollowupChannel)
	}
	if f := TaskActionFlagsFromCtx(mCtx); !f.AwaitingInput || !f.HasAny() {
		t.Error("AwaitingInput flag not recorded")
	}
	if c := mb.taskStore.comments[taskID]; len(c) != 1 || !strings.Contains(c[0].Content, "Which quarter?") {
		t.Errorf("question not saved as comment: %+v", c)
	}
	if len(mb.inbound) != 1 || mb.inbound[0].SenderID != SenderTaskFeedback || mb.inbound[0].AgentID != "lead-agent" ||
		!strings.Contains(mb.inbound[0].Content, `action="answer"`) {
		t.Errorf("lead not notified correctly: %+v", mb.inbound)
	}
}

func TestClarify_Guards(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	running := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, false, "")
	pending := makeFeedbackTask(t, mb, store.TeamTaskStatusPending, false, "")

	other := store.WithAgentID(ctx, testMember2ID)
	if res := tool.Execute(other, map[string]any{"action": "clarify", "task_id": running.String(), "text": "q"}); !res.IsError {
		t.Error("non-owner clarify must fail")
	}
	if res := tool.Execute(memberCtx(ctx), map[string]any{"action": "clarify", "task_id": pending.String(), "text": "q"}); !res.IsError {
		t.Error("clarify on a pending task must fail")
	}
	if res := tool.Execute(memberCtx(ctx), map[string]any{"action": "clarify", "task_id": running.String(), "text": "q", "kind": "other"}); !res.IsError {
		t.Error("unknown kind must fail")
	}
	if got := readTask(mb, running).Status; got != store.TeamTaskStatusInProgress {
		t.Errorf("status = %q, want in_progress", got)
	}
}

// Hai vòng clarify rồi answer liên tiếp: mỗi lần answer là dispatch có chủ ý
// nên bộ đếm của circuit breaker về 0, task không bị đánh failed.
func TestClarifyAnswer_TwoRoundsDoNotTripCircuitBreaker(t *testing.T) {
	mb, tool, leadID, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")
	readTask(mb, taskID).Metadata["dispatch_count"] = float64(maxTaskDispatches - 1)
	leadCtx := WithTaskActionFlags(store.WithAgentID(ctx, leadID), &TaskActionFlags{})

	for round := 1; round <= 2; round++ {
		if res := tool.Execute(memberCtx(ctx), map[string]any{"action": "clarify", "task_id": taskID.String(), "text": "question?"}); res.IsError {
			t.Fatalf("round %d clarify: %s", round, res.ForLLM)
		}
		if res := tool.Execute(leadCtx, map[string]any{"action": "answer", "task_id": taskID.String(), "text": "answer " + string(rune('0'+round))}); res.IsError {
			t.Fatalf("round %d answer: %s", round, res.ForLLM)
		}
		if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusInProgress {
			t.Fatalf("round %d status = %q, want in_progress", round, got)
		}
	}
	if len(mb.dispatches) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(mb.dispatches))
	}
	for i, d := range mb.dispatches {
		if d.AgentID != testMemberID || d.DispatchCount != 0 {
			t.Errorf("dispatch %d = %+v, want owner with dispatch_count 0", i, d)
		}
	}
	c := taskContract(t, mb, taskID)
	if len(c.Amendments) != 2 || c.Amendments[1].Kind != AmendmentKindAnswer || c.Amendments[1].By != "lead-agent" || c.Version != 3 {
		t.Errorf("amendments = %+v (v%d), want 2 answers, v3", c.Amendments, c.Version)
	}
	if readTask(mb, taskID).FollowupAt != nil {
		t.Error("follow-up must be cleared after answer")
	}
}

func TestAnswer_WithoutContractStoresComment(t *testing.T) {
	mb, tool, leadID, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusAwaitingInput, false, "")
	res := tool.Execute(store.WithAgentID(ctx, leadID), map[string]any{"action": "answer", "task_id": taskID.String(), "text": "Q3 2026"})
	if res.IsError {
		t.Fatalf("answer: %s", res.ForLLM)
	}
	if c := mb.taskStore.comments[taskID]; len(c) != 1 || !strings.Contains(c[0].Content, "Q3 2026") {
		t.Errorf("answer not stored as comment: %+v", c)
	}
	if TaskContractFromTask(readTask(mb, taskID)) != nil {
		t.Error("answer must not create a contract")
	}
}

func TestAnswer_ReassignAddsAmendment(t *testing.T) {
	mb, tool, leadID, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusAwaitingInput, true, "")
	res := tool.Execute(store.WithAgentID(ctx, leadID), map[string]any{
		"action": "answer", "task_id": taskID.String(), "text": "member2 has the tool", "assignee": "member2-agent",
	})
	if res.IsError {
		t.Fatalf("answer: %s", res.ForLLM)
	}
	if len(mb.dispatches) != 1 || mb.dispatches[0].AgentID != testMember2ID {
		t.Fatalf("dispatches = %+v, want member2", mb.dispatches)
	}
	c := taskContract(t, mb, taskID)
	if len(c.Amendments) != 2 || c.Amendments[1].Kind != AmendmentKindReassigned {
		t.Errorf("amendments = %+v, want answer + reassigned", c.Amendments)
	}
}

func TestAnswer_Guards(t *testing.T) {
	mb, tool, leadID, _, ctx := newTestTeamSetup()
	waiting := makeFeedbackTask(t, mb, store.TeamTaskStatusAwaitingInput, false, "")
	running := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, false, "")

	// Member, kể cả ở kênh teammate (RequireLead bỏ qua kênh này), không answer được.
	if res := tool.Execute(memberCtx(ctx), map[string]any{"action": "answer", "task_id": waiting.String(), "text": "x"}); !res.IsError {
		t.Error("member answer must fail")
	}
	lead := store.WithAgentID(ctx, leadID)
	if res := tool.Execute(lead, map[string]any{"action": "answer", "task_id": running.String(), "text": "x"}); !res.IsError {
		t.Error("answer on a task that is not awaiting_input must fail")
	}
	if res := tool.Execute(lead, map[string]any{"action": "answer", "task_id": waiting.String(), "text": ""}); !res.IsError {
		t.Error("empty answer must fail")
	}
	if res := tool.Execute(lead, map[string]any{"action": "answer", "task_id": waiting.String(), "text": "x", "assignee": "lead-agent"}); !res.IsError {
		t.Error("answer reassigning to the lead must fail")
	}
	if got := readTask(mb, waiting).Status; got != store.TeamTaskStatusAwaitingInput {
		t.Errorf("status = %q, want awaiting_input", got)
	}
}

// Lead và member gọi approve/reject trên task chờ user duyệt đều bị từ chối,
// ở mọi kênh.
func TestApproveReject_UserCheckpointBlockedForAgents(t *testing.T) {
	for _, action := range []string{"approve", "reject"} {
		for _, actor := range []uuid.UUID{testLeadID, testMemberID} {
			for _, channel := range []string{"telegram", ChannelTeammate, ChannelSystem, ChannelDashboard} {
				mb, tool, _, _, ctx := newTestTeamSetup()
				taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingUser)
				c := WithToolChannel(store.WithAgentID(ctx, actor), channel)
				res := tool.Execute(c, map[string]any{"action": action, "task_id": taskID.String(), "text": "x"})
				if !res.IsError || !strings.Contains(res.ForLLM, "/approve 7") {
					t.Errorf("%s by %s on %s: want refusal naming /approve, got %q", action, actor, channel, res.ForLLM)
				}
				if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusInReview {
					t.Errorf("%s by %s on %s: status = %q, want in_review", action, actor, channel, got)
				}
			}
		}
	}
}

func TestReject_LeadCheckpointAddsChangesRequestedAmendment(t *testing.T) {
	mb, tool, leadID, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingLead)
	res := tool.Execute(store.WithAgentID(ctx, leadID), map[string]any{"action": "reject", "task_id": taskID.String(), "text": "Add sources"})
	if res.IsError {
		t.Fatalf("reject: %s", res.ForLLM)
	}
	task := readTask(mb, taskID)
	if task.Status != store.TeamTaskStatusInProgress || TaskCheckpointPending(task) != "" {
		t.Errorf("status = %q pending=%q, want in_progress without checkpoint", task.Status, TaskCheckpointPending(task))
	}
	c := taskContract(t, mb, taskID)
	if len(c.Amendments) != 1 || c.Amendments[0].Kind != AmendmentKindChangesRequested || c.Amendments[0].Text != "Add sources" {
		t.Errorf("amendments = %+v, want changes_requested", c.Amendments)
	}
}

func TestApproveUserCheckpoint_CompletesAndDispatchesDependents(t *testing.T) {
	mb, _, _, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingUser)
	task, _ := mb.taskStore.GetTask(ctx, taskID)
	if err := ApproveUserCheckpoint(ctx, mb, mb.team, task, TaskActor{UserID: "42"}); err != nil {
		t.Fatalf("ApproveUserCheckpoint: %v", err)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusCompleted || TaskCheckpointPending(got) != "" {
		t.Errorf("status = %q pending=%q, want completed without checkpoint", got.Status, TaskCheckpointPending(got))
	}
	if mb.unblockedDispatches != 1 {
		t.Errorf("DispatchUnblockedTasks calls = %d, want 1", mb.unblockedDispatches)
	}
	if len(mb.inbound) != 1 || mb.inbound[0].AgentID != "lead-agent" {
		t.Errorf("lead not notified: %+v", mb.inbound)
	}
}

func TestRequestTaskChanges_RedispatchesWithAmendment(t *testing.T) {
	mb, _, _, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingUser)
	task, _ := mb.taskStore.GetTask(ctx, taskID)
	if err := RequestTaskChanges(ctx, mb, mb.team, task, "Use the 2026 numbers", TaskActor{UserID: "42"}); err != nil {
		t.Fatalf("RequestTaskChanges: %v", err)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusInProgress || TaskCheckpointPending(got) != "" {
		t.Errorf("status = %q pending=%q, want in_progress without checkpoint", got.Status, TaskCheckpointPending(got))
	}
	c := taskContract(t, mb, taskID)
	if len(c.Amendments) != 1 || c.Amendments[0].Kind != AmendmentKindChangesRequested || c.Amendments[0].By != "user" {
		t.Errorf("amendments = %+v, want changes_requested by user", c.Amendments)
	}
	if len(mb.dispatches) != 1 || mb.dispatches[0].DispatchCount != 0 {
		t.Errorf("dispatches = %+v, want one with reset counter", mb.dispatches)
	}
	if err := RequestTaskChanges(ctx, mb, mb.team, got, "again", TaskActor{UserID: "42"}); err == nil {
		t.Error("RequestTaskChanges on a running task must fail")
	}
}

func TestLitePolicy_BlocksClarifyAndAnswer(t *testing.T) {
	p := LiteTeamPolicy{}
	for _, a := range []string{"clarify", "answer"} {
		if p.IsAllowed(a) {
			t.Errorf("lite must block %s", a)
		}
	}
	if !(FullTeamPolicy{}).IsAllowed("clarify") || !strings.Contains(strings.Join(fullActions, ","), "answer") {
		t.Error("full policy must expose clarify and answer")
	}
}

func TestResolveCreateCheckpoint(t *testing.T) {
	newC := func() *TaskContract { c, _ := NewTaskContractFromInput(validContractArgs()); return c }

	c := newC()
	if meta, note := resolveCreateCheckpoint(c, true, true); meta != "" || note != "" || c.Checkpoint != TaskContractCheckpointResult {
		t.Errorf("contract, enabled channel: checkpoint=%q meta=%q note=%q", c.Checkpoint, meta, note)
	}
	c = newC()
	c.Checkpoint = TaskContractCheckpointResult
	if meta, note := resolveCreateCheckpoint(c, false, false); meta != "" || note == "" || c.Checkpoint != "" {
		t.Errorf("contract, unsupported channel must drop: checkpoint=%q meta=%q note=%q", c.Checkpoint, meta, note)
	}
	if meta, note := resolveCreateCheckpoint(nil, true, true); meta != TaskContractCheckpointResult || note != "" {
		t.Errorf("no contract, enabled channel: meta=%q note=%q, want result", meta, note)
	}
	if meta, note := resolveCreateCheckpoint(nil, true, false); meta != "" || note == "" {
		t.Errorf("no contract, unsupported channel must drop: meta=%q note=%q", meta, note)
	}
	if meta, note := resolveCreateCheckpoint(nil, false, true); meta != "" || note != "" {
		t.Errorf("no flag, no contract: meta=%q note=%q", meta, note)
	}
}

// TaskCheckpoint ưu tiên contract.checkpoint, rồi tới metadata.checkpoint.
func TestTaskCheckpoint_ContractThenMetadata(t *testing.T) {
	withC, _ := NewTaskContractFromInput(validContractArgs())
	withC.Checkpoint = TaskContractCheckpointResult
	noCheckpoint, _ := NewTaskContractFromInput(validContractArgs())
	cases := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"none", nil, ""},
		{"metadata only", map[string]any{TaskMetaCheckpoint: "result"}, TaskContractCheckpointResult},
		{"metadata unknown value", map[string]any{TaskMetaCheckpoint: "before_start"}, ""},
		{"contract result", map[string]any{TaskMetaContract: withC.ToMetadata()}, TaskContractCheckpointResult},
		// Task có contract: contract quyết định, metadata.checkpoint bị bỏ qua.
		{"contract wins over metadata", map[string]any{TaskMetaContract: noCheckpoint.ToMetadata(), TaskMetaCheckpoint: "result"}, ""},
	}
	for _, tc := range cases {
		if got := TaskCheckpoint(&store.TeamTaskData{Metadata: tc.meta}); got != tc.want {
			t.Errorf("%s: TaskCheckpoint = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRenderTaskContract_UnclearPointsToClarify(t *testing.T) {
	c, _ := NewTaskContractFromInput(validContractArgs())
	out := RenderTaskContract(&store.TeamTaskData{TaskNumber: 3}, c, nil)
	if !strings.Contains(out, `action="clarify"`) || strings.Contains(out, "blocker") {
		t.Errorf("If unclear line must point to clarify:\n%s", out)
	}
}

// Tin dispatch chỉ gợi ý clarify khi policy cho phép; bản Lite giữ gợi ý blocker cũ.
func TestDispatchInstructions_FollowActionPolicy(t *testing.T) {
	m := &TeamToolManager{}
	if !m.actionAllowed("clarify") {
		t.Error("nil policy behaves as full policy")
	}
	m.SetActionPolicy(LiteTeamPolicy{})
	lite := memberDispatchInstructions(m.actionAllowed("clarify"))
	if strings.Contains(lite, "clarify") || !strings.Contains(lite, "when BLOCKED and need leader input") {
		t.Errorf("lite instructions must not mention clarify:\n%s", lite)
	}
	m.SetActionPolicy(FullTeamPolicy{})
	if full := memberDispatchInstructions(m.actionAllowed("clarify")); !strings.Contains(full, `action=\"clarify\"`) && !strings.Contains(full, `action="clarify"`) {
		t.Errorf("full instructions must mention clarify:\n%s", full)
	}
}

// Tool complete đi qua cổng duyệt của user, cho cả task có và không có contract:
// task có checkpoint "result" vào in_review chờ user, không completed.
func TestComplete_CheckpointSubmitsForUserApproval(t *testing.T) {
	for _, withContract := range []bool{true, false} {
		mb, tool, _, _, ctx := newTestTeamSetup()
		mb.approvalChannels = map[string]bool{"telegram": true}
		taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, withContract, "")
		task := readTask(mb, taskID)
		if withContract {
			c := TaskContractFromTask(task)
			c.Checkpoint = TaskContractCheckpointResult
			task.Metadata[TaskMetaContract] = c.ToMetadata()
		} else {
			task.Metadata[TaskMetaCheckpoint] = TaskContractCheckpointResult
		}
		mCtx := memberCtx(ctx)
		res := tool.Execute(mCtx, map[string]any{"action": "complete", "task_id": taskID.String(), "result": "report ready"})
		if res.IsError || !strings.Contains(res.ForLLM, "user's approval") {
			t.Fatalf("contract=%v: complete = %q", withContract, res.ForLLM)
		}
		got := readTask(mb, taskID)
		if got.Status != store.TeamTaskStatusInReview || TaskCheckpointPending(got) != CheckpointPendingUser ||
			got.Result == nil || *got.Result != "report ready" {
			t.Errorf("contract=%v: status=%q pending=%q, want in_review waiting for user with result", withContract, got.Status, TaskCheckpointPending(got))
		}
		if len(mb.approvalNotices) != 1 || !TaskActionFlagsFromCtx(mCtx).Completed {
			t.Errorf("contract=%v: notices=%v flags=%+v", withContract, mb.approvalNotices, TaskActionFlagsFromCtx(mCtx))
		}
		// /approve sau đó mới hoàn thành task.
		if err := ApproveUserCheckpoint(ctx, mb, mb.team, got, TaskActor{UserID: "42"}); err != nil {
			t.Fatalf("contract=%v: approve: %v", withContract, err)
		}
		if readTask(mb, taskID).Status != store.TeamTaskStatusCompleted {
			t.Errorf("contract=%v: approve did not complete the task", withContract)
		}
	}
}

// Task không có checkpoint vẫn completed như cũ khi gọi complete.
func TestComplete_WithoutCheckpointCompletes(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")
	res := tool.Execute(memberCtx(ctx), map[string]any{"action": "complete", "task_id": taskID.String(), "result": "done"})
	if res.IsError || readTask(mb, taskID).Status != store.TeamTaskStatusCompleted || len(mb.approvalNotices) != 0 {
		t.Fatalf("complete without checkpoint: %q status=%q", res.ForLLM, readTask(mb, taskID).Status)
	}
}

// needsBackend thêm cổng needs (teamTaskNeedsChecker) vào mock: assignee nằm
// trong lacking thì thiếu tool, Satisfying là các member khác có đủ.
type needsBackend struct {
	*mockBackend
	lacking    map[uuid.UUID]bool
	satisfying []string
}

func (n *needsBackend) CheckAssignee(_ context.Context, _ *store.TeamData, _ []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID, _ string, _ bool) (string, error) {
	if contract == nil || len(contract.Needs) == 0 || !n.lacking[assigneeID] {
		return "", nil
	}
	return "", &TaskNeedsError{Assignee: n.AgentKeyFromID(context.Background(), assigneeID), Missing: contract.Needs, Satisfying: n.satisfying}
}

// makeNeedsTask tạo task của member trong team đã bật contract, với needs=web.
func makeNeedsTask(t *testing.T, mb *mockBackend, status string) uuid.UUID {
	t.Helper()
	mb.team.Settings = []byte(contractEnabledSettings)
	id := makeFeedbackTask(t, mb, status, true, "")
	task := readTask(mb, id)
	c := TaskContractFromTask(task)
	c.Needs = []string{"web"}
	task.Metadata[TaskMetaContract] = c.ToMetadata()
	return id
}

// clarify missing_tool: tin báo lead kèm member có đủ tool trong needs (cổng của phase 3).
func TestClarifyMissingTool_ListsMembersWithNeeds(t *testing.T) {
	mb, _, _, _, ctx := newTestTeamSetup()
	nb := &needsBackend{mockBackend: mb, lacking: map[uuid.UUID]bool{testMemberID: true}, satisfying: []string{"member2-agent"}}
	tool := NewTeamTasksTool(nb, FullTeamPolicy{})
	taskID := makeNeedsTask(t, mb, store.TeamTaskStatusInProgress)

	res := tool.Execute(memberCtx(ctx), map[string]any{"action": "clarify", "task_id": taskID.String(), "text": "no web_fetch", "kind": "missing_tool"})
	if res.IsError {
		t.Fatalf("clarify: %s", res.ForLLM)
	}
	if len(mb.inbound) != 1 || !strings.Contains(mb.inbound[0].Content, "[Missing tool]") || !strings.Contains(mb.inbound[0].Content, "member2-agent") {
		t.Errorf("lead notice must list members with the needed tools: %+v", mb.inbound)
	}
}

// answer dispatch lại qua cổng needs: giữ owner thiếu tool thì bị từ chối,
// giao cho member có đủ thì được.
func TestAnswer_RedispatchChecksNeeds(t *testing.T) {
	mb, _, leadID, _, ctx := newTestTeamSetup()
	nb := &needsBackend{mockBackend: mb, lacking: map[uuid.UUID]bool{testMemberID: true}, satisfying: []string{"member2-agent"}}
	tool := NewTeamTasksTool(nb, FullTeamPolicy{})
	taskID := makeNeedsTask(t, mb, store.TeamTaskStatusAwaitingInput)
	lead := store.WithAgentID(ctx, leadID)

	res := tool.Execute(lead, map[string]any{"action": "answer", "task_id": taskID.String(), "text": "try again"})
	if !res.IsError || !strings.Contains(res.ForLLM, "member2-agent") {
		t.Fatalf("answer to owner lacking needs must fail naming alternatives: %q", res.ForLLM)
	}
	if readTask(mb, taskID).Status != store.TeamTaskStatusAwaitingInput || len(mb.dispatches) != 0 {
		t.Fatal("rejected answer must not dispatch")
	}
	res = tool.Execute(lead, map[string]any{"action": "answer", "task_id": taskID.String(), "text": "member2 has web", "assignee": "member2-agent"})
	if res.IsError || len(mb.dispatches) != 1 || mb.dispatches[0].AgentID != testMember2ID {
		t.Fatalf("answer reassigning to a member with the tools: %q %+v", res.ForLLM, mb.dispatches)
	}
}

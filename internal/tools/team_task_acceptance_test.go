package tools

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestParseAcceptanceCriteria(t *testing.T) {
	got, err := ParseAcceptanceCriteria([]any{
		map[string]any{"index": float64(2), "met": "false", "evidence": " no sources "},
		map[string]any{"index": "1", "met": true},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Index != 1 || !got[0].Met || got[1].Index != 2 || got[1].Met || got[1].Evidence != "no sources" {
		t.Fatalf("parsed = %+v", got)
	}
	if got, err := ParseAcceptanceCriteria(`[{"index":1,"met":true}]`, 1); err != nil || len(got) != 1 {
		t.Fatalf("JSON string form: %+v, %v", got, err)
	}
	if got, err := ParseAcceptanceCriteria(nil, 2); err != nil || got != nil {
		t.Fatalf("nil criteria = %+v, %v", got, err)
	}
	bad := map[string]any{
		"out of range": []any{map[string]any{"index": float64(3), "met": true}},
		"zero index":   []any{map[string]any{"index": float64(0), "met": true}},
		"fraction":     []any{map[string]any{"index": 1.5, "met": true}},
		"duplicate":    []any{map[string]any{"index": float64(1), "met": true}, map[string]any{"index": float64(1), "met": false}},
		"met missing":  []any{map[string]any{"index": float64(1)}},
		"not array":    map[string]any{"index": float64(1)},
		"bad item":     []any{"criterion 1 met"},
		"long":         []any{map[string]any{"index": float64(1), "met": true, "evidence": strings.Repeat("x", taskContractMaxString+1)}},
	}
	for name, raw := range bad {
		if _, err := ParseAcceptanceCriteria(raw, 2); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCheckAcceptance(t *testing.T) {
	c := &TaskContract{Acceptance: []string{"a", "b", "c"}}
	cases := []struct {
		name     string
		criteria []AcceptanceCriterionReport
		want     AcceptanceVerdict
		missing  int
		unmet    int
	}{
		{"all met", []AcceptanceCriterionReport{{1, true, ""}, {2, true, ""}, {3, true, ""}}, AcceptanceAllMet, 0, 0},
		{"none reported", nil, AcceptanceMissing, 3, 0},
		{"one missing", []AcceptanceCriterionReport{{1, true, ""}, {3, true, ""}}, AcceptanceMissing, 1, 0},
		{"missing beats unmet", []AcceptanceCriterionReport{{1, false, ""}, {2, true, ""}}, AcceptanceMissing, 1, 1},
		{"one unmet", []AcceptanceCriterionReport{{1, true, ""}, {2, false, ""}, {3, true, ""}}, AcceptanceUnmet, 0, 1},
	}
	for _, tc := range cases {
		got := CheckAcceptance(c, tc.criteria)
		if got.Verdict != tc.want || len(got.Missing) != tc.missing || len(got.Unmet) != tc.unmet {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}

// enableContract bật task_contract cho team của mock.
func enableContract(mb *mockBackend) {
	mb.team.Settings = json.RawMessage(contractEnabledSettings)
	mb.taskStore.team = mb.team
}

func completeArgs(taskID string, criteria []any) map[string]any {
	args := map[string]any{"action": "complete", "task_id": taskID, "result": "report ready"}
	if criteria != nil {
		args["criteria"] = criteria
	}
	return args
}

func crit(index int, met bool) map[string]any {
	return map[string]any{"index": float64(index), "met": met, "evidence": "see report.md"}
}

func TestComplete_ContractAllMetCompletesWithReport(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")

	res := tool.Execute(memberCtx(ctx), completeArgs(taskID.String(), []any{crit(1, true), crit(2, true)}))
	if res.IsError {
		t.Fatalf("complete failed: %s", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.Result == nil || !strings.Contains(*got.Result, "1. [MET] Covers 4 regions — evidence: see report.md") {
		t.Errorf("result must carry the criteria table: %v", got.Result)
	}
	if rep, _ := got.Metadata[TaskMetaAcceptanceReport].([]any); len(rep) != 2 {
		t.Errorf("acceptance_report = %v", got.Metadata[TaskMetaAcceptanceReport])
	}
}

func TestComplete_ContractMissingOrUnmetWaitsForLead(t *testing.T) {
	for name, criteria := range map[string][]any{
		"no criteria": nil,
		"one missing": {crit(1, true)},
		"one unmet":   {crit(1, true), crit(2, false)},
	} {
		t.Run(name, func(t *testing.T) {
			mb, tool, _, _, ctx := newTestTeamSetup()
			enableContract(mb)
			taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")
			mCtx := memberCtx(ctx)

			res := tool.Execute(mCtx, completeArgs(taskID.String(), criteria))
			if res.IsError || !strings.Contains(res.ForLLM, "waiting for the lead's review") {
				t.Fatalf("complete = %q", res.ForLLM)
			}
			got := readTask(mb, taskID)
			if got.Status != store.TeamTaskStatusInReview || TaskCheckpointPending(got) != CheckpointPendingLead {
				t.Fatalf("status=%q pending=%q, want in_review waiting for lead", got.Status, TaskCheckpointPending(got))
			}
			if _, ok := got.Metadata[TaskMetaAcceptanceReport]; !ok {
				t.Error("acceptance_report not saved")
			}
			if f := TaskActionFlagsFromCtx(mCtx); !f.Completed {
				t.Error("Completed flag must be set so post-turn does not touch the task")
			}
		})
	}
}

func TestComplete_ContractInvalidCriteriaKeepsTaskRunning(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")

	res := tool.Execute(memberCtx(ctx), completeArgs(taskID.String(), []any{crit(5, true)}))
	if !res.IsError || !strings.Contains(res.ForLLM, "Example:") {
		t.Fatalf("expected a fixable error, got %q", res.ForLLM)
	}
	if got := readTask(mb, taskID); got.Status != store.TeamTaskStatusInProgress {
		t.Errorf("status = %q, want in_progress", got.Status)
	}
}

// Team chưa bật contract: task có contract vẫn hoàn thành như cũ, criteria bị bỏ qua.
func TestComplete_ContractDisabledTeamUnchanged(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")

	res := tool.Execute(memberCtx(ctx), completeArgs(taskID.String(), nil))
	if res.IsError {
		t.Fatalf("complete failed: %s", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusCompleted || *got.Result != "report ready" {
		t.Fatalf("status=%q result=%v, want completed with the result unchanged", got.Status, got.Result)
	}
}

func TestComplete_NoContractUnchanged(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, false, "")

	res := tool.Execute(memberCtx(ctx), completeArgs(taskID.String(), nil))
	if res.IsError {
		t.Fatalf("complete failed: %s", res.ForLLM)
	}
	if got := readTask(mb, taskID); got.Status != store.TeamTaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
}

// setContractCheckpoint đặt contract.checkpoint = "result" cho task.
func setContractCheckpoint(t *testing.T, mb *mockBackend, taskID uuid.UUID) {
	t.Helper()
	task := readTask(mb, taskID)
	c := TaskContractFromTask(task)
	c.Checkpoint = TaskContractCheckpointResult
	task.Metadata[TaskMetaContract] = c.ToMetadata()
}

func TestComplete_ContractAllMetWithCheckpointWaitsForUser(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	mb.approvalChannels = map[string]bool{"telegram": true}
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, true, "")
	setContractCheckpoint(t, mb, taskID)

	res := tool.Execute(memberCtx(ctx), completeArgs(taskID.String(), []any{crit(1, true), crit(2, true)}))
	if res.IsError || !strings.Contains(res.ForLLM, "user's approval") {
		t.Fatalf("complete = %q", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusInReview || TaskCheckpointPending(got) != CheckpointPendingUser {
		t.Fatalf("status=%q pending=%q, want waiting for user", got.Status, TaskCheckpointPending(got))
	}
	if _, ok := got.Metadata[TaskMetaAcceptanceReport]; !ok {
		t.Error("acceptance_report must be written with the user checkpoint marker")
	}
	if len(mb.approvalNotices) != 1 {
		t.Errorf("approval notices = %v, want 1", mb.approvalNotices)
	}
}

// Lead approve một task chờ lead xem mà cần user duyệt: task chuyển sang chờ
// user, không thành completed; cổng duyệt của user không bị bỏ qua.
func TestApprove_LeadReviewWithCheckpointForwardsToUser(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	mb.approvalChannels = map[string]bool{"telegram": true}
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingLead)
	setContractCheckpoint(t, mb, taskID)

	res := tool.Execute(ctx, map[string]any{"action": "approve", "task_id": taskID.String()})
	if res.IsError || !strings.Contains(res.ForLLM, "/approve 7") {
		t.Fatalf("approve = %q", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusInReview || TaskCheckpointPending(got) != CheckpointPendingUser {
		t.Fatalf("status=%q pending=%q, want in_review waiting for user", got.Status, TaskCheckpointPending(got))
	}
	if len(mb.approvalNotices) != 1 {
		t.Errorf("approval notices = %v, want 1", mb.approvalNotices)
	}
	// Từ đây agent không duyệt thay user được.
	res = tool.Execute(ctx, map[string]any{"action": "approve", "task_id": taskID.String()})
	if !res.IsError {
		t.Fatalf("second approve must be refused, got %q", res.ForLLM)
	}
}

// Task đã rời trạng thái chờ lead ở đường khác (ví dụ dashboard vừa duyệt)
// giữa lúc đọc và lúc ghi: chuyển tiếp báo xung đột, không gắn lại marker user.
func TestForwardLeadReview_ConflictLeavesTaskUntouched(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	mb.approvalChannels = map[string]bool{"telegram": true}
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingLead)
	setContractCheckpoint(t, mb, taskID)
	stale := *readTask(mb, taskID)
	stale.Metadata = maps.Clone(stale.Metadata)

	done := readTask(mb, taskID)
	done.Status = store.TeamTaskStatusCompleted
	delete(done.Metadata, TaskMetaCheckpointPending)
	done.Metadata["other"] = "kept"

	team := mb.team
	res := tool.forwardLeadReviewToUser(ctx, team, &stale, testLeadID)
	if !res.IsError || !strings.Contains(res.ForLLM, "no longer waiting") {
		t.Fatalf("forward = %q, want a conflict error", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusCompleted || TaskCheckpointPending(got) != "" || got.Metadata["other"] != "kept" {
		t.Fatalf("task changed after conflict: status=%q meta=%v", got.Status, got.Metadata)
	}
	if len(mb.approvalNotices) != 0 {
		t.Errorf("no approval notice expected, got %v", mb.approvalNotices)
	}
}

func TestApprove_LeadReviewWithoutCheckpointCompletes(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	enableContract(mb)
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, CheckpointPendingLead)

	res := tool.Execute(ctx, map[string]any{"action": "approve", "task_id": taskID.String()})
	if res.IsError {
		t.Fatalf("approve failed: %s", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusCompleted || TaskCheckpointPending(got) != "" {
		t.Fatalf("status=%q pending=%q, want completed without marker", got.Status, TaskCheckpointPending(got))
	}
}

func TestCreate_ClampsContractMaxIterations(t *testing.T) {
	for _, tc := range []struct {
		name            string
		contract, agent int
		want            int
		note            bool
	}{
		{"above agent limit", 50, 25, 25, true},
		{"below agent limit", 10, 25, 10, false},
		{"agent has no limit", 50, 0, 50, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mb, tool, _, _, ctx := newTestTeamSetup()
			enableContract(mb)
			mb.agents[testMemberID].MaxToolIterations = tc.agent
			ptd := NewPendingTeamDispatch()
			ptd.MarkListed()
			args := validContractArgs()
			args["limits"] = map[string]any{"max_iterations": float64(tc.contract)}
			res := tool.Execute(WithPendingTeamDispatch(ctx, ptd), map[string]any{
				"action": "create", "subject": "Q3 summary", "description": "Board brief",
				"assignee": "member-agent", "contract": args,
			})
			if res.IsError {
				t.Fatalf("create failed: %s", res.ForLLM)
			}
			if strings.Contains(res.ForLLM, "max_iterations was lowered") != tc.note {
				t.Errorf("note presence = %v, want %v: %s", !tc.note, tc.note, res.ForLLM)
			}
			var task *store.TeamTaskData
			for _, tk := range mb.taskStore.tasks {
				task = tk
			}
			if got := TaskContractMaxIterations(TaskContractFromTask(task)); got != tc.want {
				t.Errorf("stored max_iterations = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRenderTaskContract_IterationLimit(t *testing.T) {
	task := &store.TeamTaskData{TaskNumber: 3}
	c := &TaskContract{Objective: "o", Acceptance: []string{"a"}, Limits: &TaskContractLimits{MaxIterations: 10}}
	if out := RenderTaskContract(task, c, nil); !strings.Contains(out, "at most 10 tool iterations") {
		t.Errorf("limit line missing:\n%s", out)
	}
	c.Limits = nil
	if out := RenderTaskContract(task, c, nil); strings.Contains(out, "Iteration limit") {
		t.Errorf("unexpected limit line:\n%s", out)
	}
}

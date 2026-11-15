package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// contractPromptTeamStore chỉ cài GetTask và GetTeam. getTaskErr giả lập DB lỗi.
type contractPromptTeamStore struct {
	store.TeamStore
	tasks      map[uuid.UUID]*store.TeamTaskData
	teams      map[uuid.UUID]*store.TeamData
	getTaskErr error
}

func (s *contractPromptTeamStore) GetTask(_ context.Context, id uuid.UUID) (*store.TeamTaskData, error) {
	if s.getTaskErr != nil {
		return nil, s.getTaskErr
	}
	if t, ok := s.tasks[id]; ok {
		return t, nil
	}
	return nil, store.ErrTaskNotFound
}

func (s *contractPromptTeamStore) GetTeam(_ context.Context, id uuid.UUID) (*store.TeamData, error) {
	if t, ok := s.teams[id]; ok {
		return t, nil
	}
	return nil, errors.New("team not found")
}

func TestTeammateContractPrompt(t *testing.T) {
	contract, err := tools.ParseTaskContract(map[string]any{
		"objective": "Brief the board", "deliverables": []any{"q3.md"}, "acceptance": []any{"All regions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withID, plainID := uuid.New(), uuid.New()
	st := &contractPromptTeamStore{tasks: map[uuid.UUID]*store.TeamTaskData{
		withID:  {BaseModel: store.BaseModel{ID: withID}, TaskNumber: 42, Metadata: map[string]any{tools.TaskMetaContract: contract.ToMetadata()}},
		plainID: {BaseModel: store.BaseModel{ID: plainID}, TaskNumber: 43},
	}}
	ctx := context.Background()
	teamID := uuid.NewString()

	for name, taskID := range map[string]string{
		"no task id":       "",
		"bad task id":      "not-a-uuid",
		"unknown task":     uuid.NewString(),
		"task no contract": plainID.String(),
	} {
		if got, _, err := teammateContractPrompt(ctx, st, teamID, taskID); got != "" || err != nil {
			t.Errorf("%s: expected empty prompt and nil error, got %q, %v", name, got, err)
		}
	}
	if got, _, err := teammateContractPrompt(ctx, nil, teamID, withID.String()); got != "" || err != nil {
		t.Errorf("nil store: got %q, %v", got, err)
	}

	got, _, err := teammateContractPrompt(ctx, st, teamID, withID.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Task Contract — task #42 (v1)", "Objective: Brief the board", "1. All regions"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

// Contract ghi 10, agent cho 25: lượt chạy nhận 10 qua RunRequest.MaxIterations
// (vòng lặp chỉ hạ, không nâng giới hạn của agent).
func TestTeammateContractPrompt_MaxIterations(t *testing.T) {
	contract, err := tools.ParseTaskContract(map[string]any{
		"objective": "o", "deliverables": []any{"d"}, "acceptance": []any{"a"},
		"limits": map[string]any{"max_iterations": float64(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	limited, plain := uuid.New(), uuid.New()
	st := &contractPromptTeamStore{tasks: map[uuid.UUID]*store.TeamTaskData{
		limited: {BaseModel: store.BaseModel{ID: limited}, Metadata: map[string]any{tools.TaskMetaContract: contract.ToMetadata()}},
		plain:   {BaseModel: store.BaseModel{ID: plain}},
	}}
	if _, n, err := teammateContractPrompt(context.Background(), st, uuid.NewString(), limited.String()); err != nil || n != 10 {
		t.Errorf("max iterations = %d, %v; want 10", n, err)
	}
	if _, n, _ := teammateContractPrompt(context.Background(), st, uuid.NewString(), plain.String()); n != 0 {
		t.Errorf("task without limit: max iterations = %d, want 0", n)
	}
}

// Đọc task lỗi: team bật contract (hoặc không xác định được team) thì trả lỗi
// để lượt không chạy thiếu contract; team chưa bật thì giữ hành vi cũ.
func TestTeammateContractPrompt_GetTaskError(t *testing.T) {
	dbErr := errors.New("connection reset")
	enabledID, disabledID := uuid.New(), uuid.New()
	st := &contractPromptTeamStore{
		getTaskErr: dbErr,
		teams: map[uuid.UUID]*store.TeamData{
			enabledID:  {BaseModel: store.BaseModel{ID: enabledID}, Settings: json.RawMessage(`{"task_contract":{"enabled":true}}`)},
			disabledID: {BaseModel: store.BaseModel{ID: disabledID}},
		},
	}
	ctx := context.Background()
	taskID := uuid.NewString()

	for name, teamID := range map[string]string{
		"enabled team": enabledID.String(),
		"unknown team": uuid.NewString(),
		"bad team id":  "not-a-uuid",
	} {
		got, _, err := teammateContractPrompt(ctx, st, teamID, taskID)
		if got != "" || !errors.Is(err, dbErr) {
			t.Errorf("%s: expected wrapped DB error, got %q, %v", name, got, err)
		}
	}

	if got, _, err := teammateContractPrompt(ctx, st, disabledID.String(), taskID); got != "" || err != nil {
		t.Errorf("disabled team must keep old behavior, got %q, %v", got, err)
	}
}

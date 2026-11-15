package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// contractPromptTeamStore chỉ cài GetTask.
type contractPromptTeamStore struct {
	store.TeamStore
	tasks map[uuid.UUID]*store.TeamTaskData
}

func (s *contractPromptTeamStore) GetTask(_ context.Context, id uuid.UUID) (*store.TeamTaskData, error) {
	if t, ok := s.tasks[id]; ok {
		return t, nil
	}
	return nil, store.ErrTaskNotFound
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

	for name, taskID := range map[string]string{
		"no task id":       "",
		"bad task id":      "not-a-uuid",
		"unknown task":     uuid.NewString(),
		"task no contract": plainID.String(),
	} {
		if got := teammateContractPrompt(ctx, st, taskID); got != "" {
			t.Errorf("%s: expected empty prompt, got %q", name, got)
		}
	}
	if got := teammateContractPrompt(ctx, nil, withID.String()); got != "" {
		t.Errorf("nil store: got %q", got)
	}

	got := teammateContractPrompt(ctx, st, withID.String())
	for _, want := range []string{"## Task Contract — task #42 (v1)", "Objective: Brief the board", "1. All regions"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

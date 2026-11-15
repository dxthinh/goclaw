package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// teammateContractPrompt dựng phần system prompt bổ sung cho lượt teammate:
// khối Task Contract của task đang chạy. Contract phải nằm trong system prompt
// chứ không chỉ trong tin dispatch, vì session member dùng chung giữa các task
// và lịch sử dài bị tóm tắt.
//
// maxIterations là contract.limits.max_iterations (0 khi không đặt), dùng cho
// RunRequest.MaxIterations: vòng lặp chỉ dùng khi nó nhỏ hơn giới hạn của agent.
//
// Trả chuỗi rỗng, nil khi không có task, task id sai, task không tồn tại hoặc
// task không có contract. Khi đọc task lỗi (ví dụ DB tạm hỏng) thì trả lỗi để
// lượt đó không chạy thiếu contract, trừ khi xác nhận được team chưa bật
// task_contract: khi đó giữ hành vi cũ là chạy không có khối contract.
func teammateContractPrompt(ctx context.Context, teamStore store.TeamStore, teamID, taskID string) (prompt string, maxIterations int, err error) {
	if teamStore == nil || taskID == "" {
		return "", 0, nil
	}
	id, err := uuid.Parse(taskID)
	if err != nil {
		return "", 0, nil
	}
	task, err := teamStore.GetTask(ctx, id)
	switch {
	case err == nil && task != nil:
		c := tools.TaskContractFromTask(task)
		return tools.RenderTaskContract(task, c, tools.EditionTeamPolicy()), tools.TaskContractMaxIterations(c), nil
	case err == nil, errors.Is(err, store.ErrTaskNotFound):
		return "", 0, nil
	}
	if teamUUID, perr := uuid.Parse(teamID); perr == nil {
		if team, terr := teamStore.GetTeam(ctx, teamUUID); terr == nil && team != nil &&
			!tools.ParseTaskContractConfig(team.Settings).Enabled {
			slog.Debug("teammate contract: cannot load task, team has no task contract", "task_id", taskID, "error", err)
			return "", 0, nil
		}
	}
	return "", 0, fmt.Errorf("cannot load task contract for task %s: %w", taskID, err)
}

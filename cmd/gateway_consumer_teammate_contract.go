package cmd

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// teammateContractPrompt dựng phần system prompt bổ sung cho lượt teammate:
// khối Task Contract của task đang chạy. Contract phải nằm trong system prompt
// chứ không chỉ trong tin dispatch, vì session member dùng chung giữa các task
// và lịch sử dài bị tóm tắt. Trả chuỗi rỗng khi không có task hoặc contract.
func teammateContractPrompt(ctx context.Context, teamStore store.TeamStore, taskID string) string {
	if teamStore == nil || taskID == "" {
		return ""
	}
	id, err := uuid.Parse(taskID)
	if err != nil {
		return ""
	}
	task, err := teamStore.GetTask(ctx, id)
	if err != nil || task == nil {
		if err != nil {
			slog.Debug("teammate contract: cannot load task", "task_id", taskID, "error", err)
		}
		return ""
	}
	return tools.RenderTaskContract(task, tools.TaskContractFromTask(task))
}

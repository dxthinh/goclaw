package mcp

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// denyUserCheckpoint chặn approve/reject qua MCP với task đang chờ user duyệt
// (checkpoint_pending = "user"). MCP không phải kênh của người dùng: agent
// cũng gọi được /api/mcp/, nên không có cách chắc chắn biết người gọi là user
// đã yêu cầu task. Task đó chỉ được duyệt ở chat gốc (/approve, /changes) hoặc
// trên dashboard. Trả result lỗi khi bị chặn; nil khi cho đi tiếp. Lỗi đọc
// task được trả nguyên để không đi tiếp khi chưa biết task có chờ user không.
func denyUserCheckpoint(ctx context.Context, teams store.TeamStore, tool string, teamID, taskID uuid.UUID) (*mcpgo.CallToolResult, error) {
	task, err := getTaskInTeam(ctx, teams, teamID, taskID)
	if err != nil {
		return toolError(tool, err)
	}
	if tools.TaskCheckpointPending(task) != tools.CheckpointPendingUser {
		return nil, nil
	}
	slog.Warn("security.task_approval_denied",
		"reason", "user_checkpoint_via_mcp", "tool", tool, "channel", "mcp",
		"team_id", teamID, "task_id", taskID, "task_number", task.TaskNumber)
	return mcpgo.NewToolResultError(tool + ": task is waiting for the user's approval; it can only be approved or rejected from the origin chat or the dashboard"), nil
}

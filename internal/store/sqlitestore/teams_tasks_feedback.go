//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// AwaitTaskInput chuyển task in_progress sang awaiting_input (member hỏi lại)
// và nhả khóa để ticker không recover task đang chờ câu trả lời.
func (s *SQLiteTeamStore) AwaitTaskInput(ctx context.Context, taskID, teamID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = $1, locked_at = NULL, lock_expires_at = NULL,
		 progress_percent = NULL, progress_step = NULL, updated_at = $2
		 WHERE id = $3 AND team_id = $4 AND status = $5 AND tenant_id = $6`,
		store.TeamTaskStatusAwaitingInput, time.Now(),
		taskID, teamID, store.TeamTaskStatusInProgress, tenantIDForInsert(ctx),
	)
	if err != nil {
		return err
	}
	return requireOneRow(res, "task not in progress or not found")
}

// SubmitTaskResultForReview chuyển task in_progress sang in_review, ghi
// result và nhả khóa. Task chờ người duyệt kết quả thay vì thành completed.
func (s *SQLiteTeamStore) SubmitTaskResultForReview(ctx context.Context, taskID, teamID uuid.UUID, result string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = $1, result = $2, locked_at = NULL, lock_expires_at = NULL,
		 progress_percent = NULL, updated_at = $3
		 WHERE id = $4 AND team_id = $5 AND status = $6 AND tenant_id = $7`,
		store.TeamTaskStatusInReview, result, time.Now(),
		taskID, teamID, store.TeamTaskStatusInProgress, tenantIDForInsert(ctx),
	)
	if err != nil {
		return err
	}
	return requireOneRow(res, "task not in progress or not found")
}

// GetTaskByNumberInChat tra đúng một task theo số trong team, giới hạn ở chat
// gốc (channel, chat_id) để lệnh duyệt trong chat không chạm task của chat
// khác. Dùng index idx_tt_scope (team_id, channel, chat_id).
func (s *SQLiteTeamStore) GetTaskByNumberInChat(ctx context.Context, teamID uuid.UUID, taskNumber int, channel, chatID string) (*store.TeamTaskData, error) {
	args := []any{teamID, channel, chatID, taskNumber}
	tenantWhere := ""
	if !store.IsCrossTenant(ctx) {
		tid := store.TenantIDFromContext(ctx)
		if tid == uuid.Nil {
			return nil, fmt.Errorf("tenant_id required")
		}
		tenantWhere = " AND t.tenant_id = ?"
		args = append(args, tid)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskSelectCols+`
		 `+taskJoinClause+`
		 WHERE t.team_id = ? AND t.channel = ? AND t.chat_id = ? AND t.task_number = ?`+tenantWhere+`
		 LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks, err := scanTaskRowsJoined(rows)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, store.ErrTaskNotFound
	}
	return &tasks[0], nil
}

// requireOneRow trả lỗi msg khi câu UPDATE không chạm dòng nào.
func requireOneRow(res interface{ RowsAffected() (int64, error) }, msg string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

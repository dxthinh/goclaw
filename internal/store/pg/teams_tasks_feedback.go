package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// AwaitTaskInput chuyển task in_progress sang awaiting_input (member hỏi lại)
// và nhả khóa để ticker không recover task đang chờ câu trả lời.
func (s *PGTeamStore) AwaitTaskInput(ctx context.Context, taskID, teamID uuid.UUID) error {
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
// result, nhả khóa và trộn metadataPatch vào metadata (khóa ở cấp trên cùng)
// trong cùng một câu UPDATE. Task chờ người duyệt kết quả thay vì thành
// completed; trạng thái và marker chờ duyệt không thể ghi lệch nhau.
func (s *PGTeamStore) SubmitTaskResultForReview(ctx context.Context, taskID, teamID uuid.UUID, result string, metadataPatch map[string]any) error {
	patch, err := marshalMetadataPatch(metadataPatch)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = $1, result = $2, locked_at = NULL, lock_expires_at = NULL,
		 progress_percent = NULL, updated_at = $3, metadata = COALESCE(metadata, '{}'::jsonb) || $4::jsonb
		 WHERE id = $5 AND team_id = $6 AND status = $7 AND tenant_id = $8`,
		store.TeamTaskStatusInReview, result, time.Now(), patch,
		taskID, teamID, store.TeamTaskStatusInProgress, tenantIDForInsert(ctx),
	)
	if err != nil {
		return err
	}
	return requireOneRow(res, "task not in progress or not found")
}

// TransitionTaskCheckpointPending đổi checkpoint_pending from → to cho task
// in_review trong một câu UPDATE có điều kiện (status, marker cũ, team,
// tenant); metadata được trộn patch nên khóa khác không bị ghi đè.
func (s *PGTeamStore) TransitionTaskCheckpointPending(ctx context.Context, taskID, teamID uuid.UUID, from, to string) error {
	patch, err := marshalMetadataPatch(map[string]any{"checkpoint_pending": to})
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET metadata = COALESCE(metadata, '{}'::jsonb) || $1::jsonb, updated_at = $2
		 WHERE id = $3 AND team_id = $4 AND status = $5 AND tenant_id = $6
		   AND metadata->>'checkpoint_pending' = $7`,
		patch, time.Now(), taskID, teamID, store.TeamTaskStatusInReview, tenantIDForInsert(ctx), from,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrTaskCheckpointConflict
	}
	return nil
}

// GetTaskByNumberInChat tra đúng một task theo số trong team, giới hạn ở chat
// gốc (channel, chat_id) để lệnh duyệt trong chat không chạm task của chat
// khác. Dùng index idx_tt_scope (team_id, channel, chat_id).
func (s *PGTeamStore) GetTaskByNumberInChat(ctx context.Context, teamID uuid.UUID, taskNumber int, channel, chatID string) (*store.TeamTaskData, error) {
	args := []any{teamID, channel, chatID, taskNumber}
	tenantWhere := ""
	if !store.IsCrossTenant(ctx) {
		tid := store.TenantIDFromContext(ctx)
		if tid == uuid.Nil {
			return nil, fmt.Errorf("tenant_id required")
		}
		tenantWhere = fmt.Sprintf(" AND t.tenant_id = $%d", len(args)+1)
		args = append(args, tid)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskSelectCols+`
		 `+taskJoinClause+`
		 WHERE t.team_id = $1 AND t.channel = $2 AND t.chat_id = $3 AND t.task_number = $4`+tenantWhere+`
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

// marshalMetadataPatch mã hóa patch metadata thành JSON object; nil thành "{}".
func marshalMetadataPatch(patch map[string]any) (string, error) {
	if len(patch) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("encode metadata patch: %w", err)
	}
	return string(b), nil
}

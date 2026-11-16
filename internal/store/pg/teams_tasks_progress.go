package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ============================================================
// Progress
// ============================================================

func (s *PGTeamStore) UpdateTaskProgress(ctx context.Context, taskID, teamID uuid.UUID, percent int, step string) error {
	if percent < 0 || percent > 100 {
		return fmt.Errorf("progress percent must be 0-100, got %d", percent)
	}
	// Also renews lock_expires_at as a heartbeat.
	now := time.Now()
	lockExpires := now.Add(taskLockDuration)
	tid := tenantIDForInsert(ctx)
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET progress_percent = $1, progress_step = $2, lock_expires_at = $3, updated_at = $4
		 WHERE id = $5 AND status = $6 AND team_id = $7 AND tenant_id = $8`,
		percent, step, lockExpires, now,
		taskID, store.TeamTaskStatusInProgress, teamID, tid,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Diagnostic: log actual status to help trace why the task is not in_progress.
		if diag, _ := s.GetTask(ctx, taskID); diag != nil {
			slog.Warn("UpdateTaskProgress: status mismatch", "task_id", taskID,
				"expected", "in_progress", "actual", diag.Status, "lock_expires", diag.LockExpiresAt)
		}
		return fmt.Errorf("task not in progress or not found")
	}
	return nil
}

// RenewTaskLock extends the lock expiration for an in-progress task.
// Called periodically by the consumer as a heartbeat to prevent
// the ticker from recovering a long-running task.
func (s *PGTeamStore) RenewTaskLock(ctx context.Context, taskID, teamID uuid.UUID) error {
	now := time.Now()
	lockExpires := now.Add(taskLockDuration)
	tid := tenantIDForInsert(ctx)
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET lock_expires_at = $1, updated_at = $2
		 WHERE id = $3 AND team_id = $4 AND status = $5 AND tenant_id = $6`,
		lockExpires, now,
		taskID, teamID, store.TeamTaskStatusInProgress, tid,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task not in progress or not found")
	}
	return nil
}

// ============================================================
// Stale recovery (batch — all active teams in one query)
// ============================================================

// activeTeamJoin is the JOIN clause that filters to active teams.
const activeTeamJoin = `JOIN agent_teams tm ON tm.id = t.team_id
		 AND tm.status = 'active'`

// RecoverAllStaleTasks resets in_progress tasks with expired locks across all active teams.
func (s *PGTeamStore) RecoverAllStaleTasks(ctx context.Context) ([]store.RecoveredTaskInfo, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx,
		`UPDATE team_tasks t
		 SET status = $1, locked_at = NULL, lock_expires_at = NULL,
		     followup_at = NULL, followup_count = 0, followup_message = NULL,
		     followup_channel = NULL, followup_chat_id = NULL, updated_at = $2
		 FROM agent_teams tm
		 WHERE t.team_id = tm.id AND tm.status = 'active'
		   AND t.status = $3
		   AND t.lock_expires_at IS NOT NULL AND t.lock_expires_at < $2
		 RETURNING t.id, t.team_id, t.tenant_id, t.task_number, t.subject, COALESCE(t.channel, ''), COALESCE(t.chat_id, '')`,
		store.TeamTaskStatusPending, now, store.TeamTaskStatusInProgress,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecoveredTaskInfoRows(rows)
}

// ForceRecoverAllTasks resets ALL in_progress tasks across active teams (startup).
func (s *PGTeamStore) ForceRecoverAllTasks(ctx context.Context) ([]store.RecoveredTaskInfo, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx,
		`UPDATE team_tasks t
		 SET status = $1, locked_at = NULL, lock_expires_at = NULL,
		     followup_at = NULL, followup_count = 0, followup_message = NULL,
		     followup_channel = NULL, followup_chat_id = NULL, updated_at = $2
		 FROM agent_teams tm
		 WHERE t.team_id = tm.id AND tm.status = 'active'
		   AND t.status = $3
		 RETURNING t.id, t.team_id, t.tenant_id, t.task_number, t.subject, COALESCE(t.channel, ''), COALESCE(t.chat_id, '')`,
		store.TeamTaskStatusPending, now, store.TeamTaskStatusInProgress,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecoveredTaskInfoRows(rows)
}

// ListBusyTaskOwners trả về các owner đang giữ task in_progress trong team.
// Dùng index idx_tt_owner_status (team_id, owner_agent_id, status).
func (s *PGTeamStore) ListBusyTaskOwners(ctx context.Context, teamID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT owner_agent_id FROM team_tasks
		 WHERE team_id = $1 AND tenant_id = $2 AND status = $3 AND owner_agent_id IS NOT NULL`,
		teamID, tenantIDForInsert(ctx), store.TeamTaskStatusInProgress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListRecoverableTasks returns pending + stale-locked tasks for a single team.
// Used by DispatchUnblockedTasks after task completion.
func (s *PGTeamStore) ListRecoverableTasks(ctx context.Context, teamID uuid.UUID) ([]store.TeamTaskData, error) {
	now := time.Now()
	tid := tenantIDForInsert(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskSelectCols+`
		 `+taskJoinClause+`
		 WHERE t.team_id = $1
		   AND t.tenant_id = $6
		   AND (
		     t.status = $2
		     OR (t.status = $3 AND t.lock_expires_at IS NOT NULL AND t.lock_expires_at < $4)
		   )
		 ORDER BY t.priority DESC, t.created_at
		 LIMIT $5`,
		teamID, store.TeamTaskStatusPending, store.TeamTaskStatusInProgress, now, maxListTasksRows, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTaskRowsJoined(rows)
}

// MarkAllStaleTasks marks pending tasks older than olderThan as stale across all active teams.
// Bỏ qua task pending mà owner đang giữ một task in_progress khác trong cùng team:
// dispatch chạy một task mỗi owner, nên task đó đang xếp hàng chứ không bị bỏ quên.
func (s *PGTeamStore) MarkAllStaleTasks(ctx context.Context, olderThan time.Time) ([]store.RecoveredTaskInfo, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx,
		`UPDATE team_tasks t
		 SET status = $1, updated_at = $2
		 FROM agent_teams tm
		 WHERE t.team_id = tm.id AND tm.status = 'active'
		   AND t.status = $3 AND t.updated_at < $4
		   AND NOT EXISTS (
		     SELECT 1 FROM team_tasks busy
		     WHERE busy.team_id = t.team_id AND busy.tenant_id = t.tenant_id
		       AND busy.owner_agent_id = t.owner_agent_id
		       AND busy.status = $5 AND busy.id <> t.id
		   )
		 RETURNING t.id, t.team_id, t.tenant_id, t.task_number, t.subject, COALESCE(t.channel, ''), COALESCE(t.chat_id, '')`,
		store.TeamTaskStatusStale, now, store.TeamTaskStatusPending, olderThan, store.TeamTaskStatusInProgress,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecoveredTaskInfoRows(rows)
}

// MarkInReviewStaleTasks marks in_review tasks older than olderThan as stale across all active teams.
// Bỏ qua task đang chờ duyệt checkpoint (metadata.checkpoint_pending khác rỗng):
// task đó chờ user hoặc lead, không phải bị bỏ quên.
func (s *PGTeamStore) MarkInReviewStaleTasks(ctx context.Context, olderThan time.Time) ([]store.RecoveredTaskInfo, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx,
		`UPDATE team_tasks t
		 SET status = $1, updated_at = $2
		 FROM agent_teams tm
		 WHERE t.team_id = tm.id AND tm.status = 'active'
		   AND t.status = $3 AND t.updated_at < $4
		   AND COALESCE(t.metadata->>'checkpoint_pending', '') = ''
		 RETURNING t.id, t.team_id, t.tenant_id, t.task_number, t.subject, COALESCE(t.channel, ''), COALESCE(t.chat_id, '')`,
		store.TeamTaskStatusStale, now, store.TeamTaskStatusInReview, olderThan,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecoveredTaskInfoRows(rows)
}

// FixOrphanedBlockedTasks unblocks blocked tasks where all blockers are completed or cancelled.
// Safety net for cases where unblockDependentTasks() transaction rolled back.
// Blocker failed không tính là đã giải quyết: task phụ thuộc chờ lead retry hoặc cancel.
func (s *PGTeamStore) FixOrphanedBlockedTasks(ctx context.Context) ([]store.RecoveredTaskInfo, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx,
		`UPDATE team_tasks t
		 SET blocked_by = '{}', status = $1, updated_at = $2
		 FROM agent_teams tm
		 WHERE t.team_id = tm.id AND tm.status = 'active'
		   AND t.status = 'blocked'
		   AND array_length(t.blocked_by, 1) > 0
		   AND NOT EXISTS (
		     SELECT 1 FROM unnest(t.blocked_by) AS bid(id)
		     JOIN team_tasks bt ON bt.id = bid.id AND bt.tenant_id = t.tenant_id
		     WHERE bt.status NOT IN ($3, $4)
		   )
		 RETURNING t.id, t.team_id, t.tenant_id, t.task_number, t.subject, COALESCE(t.channel, ''), COALESCE(t.chat_id, '')`,
		store.TeamTaskStatusPending, now,
		store.TeamTaskStatusCompleted, store.TeamTaskStatusCancelled,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecoveredTaskInfoRows(rows)
}

func scanRecoveredTaskInfoRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]store.RecoveredTaskInfo, error) {
	var out []store.RecoveredTaskInfo
	for rows.Next() {
		var info store.RecoveredTaskInfo
		if err := rows.Scan(&info.ID, &info.TeamID, &info.TenantID, &info.TaskNumber, &info.Subject, &info.Channel, &info.ChatID); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReopenCompletedTask đưa task completed về pending để lead dispatch lại. Điều
// kiện status = completed nằm trong cùng câu UPDATE, nên không đường nào khác
// (retry trên dashboard, ResetTaskStatus) xoá được kết quả vừa duyệt.
func (s *PGTeamStore) ReopenCompletedTask(ctx context.Context, taskID, teamID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = $1, locked_at = NULL, lock_expires_at = NULL, result = NULL,
		 progress_percent = NULL, progress_step = NULL, updated_at = $2
		 WHERE id = $3 AND team_id = $4 AND status = $5 AND tenant_id = $6`,
		store.TeamTaskStatusPending, time.Now(),
		taskID, teamID, store.TeamTaskStatusCompleted, tenantIDForInsert(ctx),
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task not completed or wrong team")
	}
	return nil
}

func (s *PGTeamStore) ResetTaskStatus(ctx context.Context, taskID, teamID uuid.UUID) error {
	now := time.Now()
	tid := tenantIDForInsert(ctx)
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = $1, locked_at = NULL, lock_expires_at = NULL, result = NULL,
		 progress_percent = NULL, progress_step = NULL, updated_at = $2
		 WHERE id = $3 AND team_id = $4 AND status IN ($5, $6, $7, $8, $9, $11) AND tenant_id = $10`,
		store.TeamTaskStatusPending, now,
		taskID, teamID, store.TeamTaskStatusStale, store.TeamTaskStatusFailed,
		store.TeamTaskStatusCancelled, store.TeamTaskStatusInReview, store.TeamTaskStatusBlocked, tid,
		store.TeamTaskStatusAwaitingInput,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task not available for reset (not stale/failed/cancelled/in_review/awaiting_input or wrong team)")
	}
	return nil
}

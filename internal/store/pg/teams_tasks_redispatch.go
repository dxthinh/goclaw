package pg

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// RedispatchReviewedTask đưa task in_review thẳng về in_progress cho owner
// (khóa như AssignTask), xoá result và metadata.checkpoint_pending, và thay các
// khóa top-level trong metadataSet, tất cả trong một câu UPDATE có điều kiện.
// Task không bao giờ ở pending, nên DispatchUnblockedTasks không nhặt được nó
// trước khi phản hồi được lưu; task vừa được duyệt thì câu UPDATE không khớp.
func (s *PGTeamStore) RedispatchReviewedTask(ctx context.Context, taskID, teamID, ownerID uuid.UUID, metadataSet map[string]any) error {
	set, err := marshalMetadataPatch(metadataSet)
	if err != nil {
		return err
	}
	now := time.Now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = $1, owner_agent_id = $2, locked_at = $3, lock_expires_at = $4,
		 updated_at = $3, result = NULL, progress_percent = NULL, progress_step = NULL,
		 metadata = (COALESCE(metadata, '{}'::jsonb) - 'checkpoint_pending') || $5::jsonb
		 WHERE id = $6 AND team_id = $7 AND status = $8 AND tenant_id = $9`,
		store.TeamTaskStatusInProgress, ownerID, now, now.Add(taskLockDuration), set,
		taskID, teamID, store.TeamTaskStatusInReview, tenantIDForInsert(ctx),
	)
	if err != nil {
		return err
	}
	return requireOneRow(res, "task not in review or not found")
}

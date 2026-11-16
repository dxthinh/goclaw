//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// RedispatchReviewedTask đưa task in_review thẳng về in_progress cho owner
// (khóa như AssignTask), xoá result và metadata.checkpoint_pending, và thay các
// khóa top-level trong metadataSet, tất cả trong một câu UPDATE có điều kiện.
// Dùng json_set thay vì json_patch để giá trị object (ví dụ contract) được thay
// nguyên khối như toán tử || của PG, không bị trộn đệ quy.
func (s *SQLiteTeamStore) RedispatchReviewedTask(ctx context.Context, taskID, teamID, ownerID uuid.UUID, metadataSet map[string]any) error {
	metaExpr := `json_remove(COALESCE(metadata, '{}'), '$.checkpoint_pending')`
	var metaArgs []any
	if len(metadataSet) > 0 {
		keys := make([]string, 0, len(metadataSet))
		for k := range metadataSet {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var b strings.Builder
		b.WriteString("json_set(" + metaExpr)
		for _, k := range keys {
			v, err := json.Marshal(metadataSet[k])
			if err != nil {
				return fmt.Errorf("encode metadata %s: %w", k, err)
			}
			b.WriteString(", ?, json(?)")
			metaArgs = append(metaArgs, `$."`+k+`"`, string(v))
		}
		b.WriteString(")")
		metaExpr = b.String()
	}

	now := time.Now()
	args := []any{store.TeamTaskStatusInProgress, ownerID, now, now.Add(taskLockDuration), now}
	args = append(args, metaArgs...)
	args = append(args, taskID, teamID, store.TeamTaskStatusInReview, tenantIDForInsert(ctx))
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET status = ?, owner_agent_id = ?, locked_at = ?, lock_expires_at = ?,
		 updated_at = ?, result = NULL, progress_percent = NULL, progress_step = NULL,
		 metadata = `+metaExpr+`
		 WHERE id = ? AND team_id = ? AND status = ? AND tenant_id = ?`,
		args...,
	)
	if err != nil {
		return err
	}
	return requireOneRow(res, "task not in review or not found")
}

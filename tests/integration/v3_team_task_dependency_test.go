//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

func TestTeamTask_FailedUpstreamKeepsDependentBlocked(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "FailTask"
		if pending {
			name = "FailPendingTask"
		}
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			pg.InitSqlx(db)
			tenantID, agentID := seedTenantAgent(t, db)
			ctx := tenantCtx(tenantID)
			ts := pg.NewPGTeamStore(db)
			teamID, memberID := seedTeam(t, db, tenantID, agentID)

			taskA := makeTask(teamID, "upstream", store.TeamTaskStatusPending)
			if err := ts.CreateTask(ctx, taskA); err != nil {
				t.Fatalf("CreateTask A: %v", err)
			}
			taskB := makeTask(teamID, "downstream", store.TeamTaskStatusPending)
			if err := ts.CreateTask(ctx, taskB); err != nil {
				t.Fatalf("CreateTask B: %v", err)
			}
			if _, err := db.Exec(`UPDATE team_tasks SET status = 'blocked', blocked_by = $1 WHERE id = $2`,
				[]uuid.UUID{taskA.ID}, taskB.ID); err != nil {
				t.Fatalf("set blocked: %v", err)
			}

			if pending {
				if err := ts.FailPendingTask(ctx, taskA.ID, teamID, "invalid"); err != nil {
					t.Fatalf("FailPendingTask A: %v", err)
				}
			} else {
				if err := ts.ClaimTask(ctx, taskA.ID, memberID, teamID); err != nil {
					t.Fatalf("ClaimTask A: %v", err)
				}
				if err := ts.FailTask(ctx, taskA.ID, teamID, "boom"); err != nil {
					t.Fatalf("FailTask A: %v", err)
				}
			}

			gotB, err := ts.GetTask(ctx, taskB.ID)
			if err != nil {
				t.Fatalf("GetTask B: %v", err)
			}
			if gotB.Status != store.TeamTaskStatusBlocked {
				t.Errorf("B status = %q, want blocked (thượng nguồn failed không mở khóa)", gotB.Status)
			}
			if len(gotB.BlockedBy) != 1 || gotB.BlockedBy[0] != taskA.ID {
				t.Errorf("B blocked_by = %v, want [%s]", gotB.BlockedBy, taskA.ID)
			}

			// Ticker cũng không được coi failed là đã giải quyết.
			fixed, err := ts.FixOrphanedBlockedTasks(ctx)
			if err != nil {
				t.Fatalf("FixOrphanedBlockedTasks: %v", err)
			}
			for _, f := range fixed {
				if f.ID == taskB.ID {
					t.Error("FixOrphanedBlockedTasks mở khóa B dù A failed")
				}
			}

			// Retry rồi complete A thì B mở khóa như cũ.
			if err := ts.ResetTaskStatus(ctx, taskA.ID, teamID); err != nil {
				t.Fatalf("ResetTaskStatus A: %v", err)
			}
			if err := ts.AssignTask(ctx, taskA.ID, memberID, teamID); err != nil {
				t.Fatalf("AssignTask A (retry): %v", err)
			}
			if err := ts.CompleteTask(ctx, taskA.ID, teamID, "ok"); err != nil {
				t.Fatalf("CompleteTask A: %v", err)
			}
			gotB2, _ := ts.GetTask(ctx, taskB.ID)
			if gotB2.Status != store.TeamTaskStatusPending || len(gotB2.BlockedBy) != 0 {
				t.Errorf("sau khi A completed: B = %q %v, want pending []", gotB2.Status, gotB2.BlockedBy)
			}
		})
	}
}

func TestTeamTask_CancelledUpstreamStillUnblocks(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, _ := seedTeam(t, db, tenantID, agentID)

	taskA := makeTask(teamID, "upstream", store.TeamTaskStatusPending)
	taskB := makeTask(teamID, "downstream", store.TeamTaskStatusPending)
	for _, tk := range []*store.TeamTaskData{taskA, taskB} {
		if err := ts.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE team_tasks SET status = 'blocked', blocked_by = $1 WHERE id = $2`,
		[]uuid.UUID{taskA.ID}, taskB.ID); err != nil {
		t.Fatalf("set blocked: %v", err)
	}
	if err := ts.CancelTask(ctx, taskA.ID, teamID, "not needed"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	gotB, _ := ts.GetTask(ctx, taskB.ID)
	if gotB.Status != store.TeamTaskStatusPending {
		t.Errorf("B status = %q, want pending", gotB.Status)
	}
}

// Task pending xếp hàng sau task in_progress của cùng owner không bị đánh stale.
func TestTeamTask_QueuedBehindBusyOwnerNotStale(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	running := makeTask(teamID, "running", store.TeamTaskStatusPending)
	queued := makeTask(teamID, "queued", store.TeamTaskStatusPending)
	idle := makeTask(teamID, "nobody picked up", store.TeamTaskStatusPending)
	for _, tk := range []*store.TeamTaskData{running, queued, idle} {
		if err := ts.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	if err := ts.ClaimTask(ctx, running.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if _, err := db.Exec(`UPDATE team_tasks SET owner_agent_id = $1, updated_at = $2 WHERE id = $3`,
		memberID, old, queued.ID); err != nil {
		t.Fatalf("backdate queued: %v", err)
	}
	if _, err := db.Exec(`UPDATE team_tasks SET updated_at = $1 WHERE id = $2`, old, idle.ID); err != nil {
		t.Fatalf("backdate idle: %v", err)
	}

	stale, err := ts.MarkAllStaleTasks(ctx, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("MarkAllStaleTasks: %v", err)
	}
	marked := map[uuid.UUID]bool{}
	for _, s := range stale {
		marked[s.ID] = true
	}
	if marked[queued.ID] {
		t.Error("task xếp hàng sau owner đang bận bị đánh stale")
	}
	if !marked[idle.ID] {
		t.Error("task pending không owner quá hạn phải bị đánh stale")
	}
	got, _ := ts.GetTask(ctx, queued.ID)
	if got.Status != store.TeamTaskStatusPending {
		t.Errorf("queued status = %q, want pending", got.Status)
	}
}

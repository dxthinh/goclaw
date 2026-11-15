//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type depFixture struct {
	db       *sql.DB
	teams    *SQLiteTeamStore
	ctx      context.Context
	teamID   uuid.UUID
	memberID uuid.UUID
}

func newDepFixture(t *testing.T) *depFixture {
	t.Helper()
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	tenantID, leadID, memberID, teamID := store.GenNewID(), store.GenNewID(), store.GenNewID(), store.GenNewID()
	mustExec(t, db, `INSERT INTO tenants (id, name, slug, status, settings, created_at, updated_at)
		VALUES (?, 'deps', 'deps', 'active', '{}', datetime('now'), datetime('now'))`, tenantID)
	mustExec(t, db, `INSERT INTO agents (id, agent_key, owner_id, model, tenant_id) VALUES (?, 'lead', 'owner', 'gpt', ?)`, leadID, tenantID)
	mustExec(t, db, `INSERT INTO agents (id, agent_key, owner_id, model, tenant_id) VALUES (?, 'member', 'owner', 'gpt', ?)`, memberID, tenantID)
	mustExec(t, db, `INSERT INTO agent_teams (id, name, lead_agent_id, created_by, tenant_id, status)
		VALUES (?, 'team', ?, 'owner', ?, 'active')`, teamID, leadID, tenantID)
	return &depFixture{
		db: db, teams: NewSQLiteTeamStore(db), ctx: store.WithTenantID(context.Background(), tenantID),
		teamID: teamID, memberID: memberID,
	}
}

func (f *depFixture) create(t *testing.T, subject string) *store.TeamTaskData {
	t.Helper()
	task := &store.TeamTaskData{TeamID: f.teamID, Subject: subject, Status: store.TeamTaskStatusPending, Metadata: map[string]any{"source": "test"}}
	if err := f.teams.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("CreateTask %s: %v", subject, err)
	}
	return task
}

func TestTeamTaskSQLite_FailedUpstreamKeepsDependentBlocked(t *testing.T) {
	for _, pending := range []bool{false, true} {
		f := newDepFixture(t)
		a := f.create(t, "upstream")
		b := f.create(t, "downstream")
		mustExec(t, f.db, `UPDATE team_tasks SET status = 'blocked', blocked_by = ? WHERE id = ?`, `["`+a.ID.String()+`"]`, b.ID)

		if pending {
			if err := f.teams.FailPendingTask(f.ctx, a.ID, f.teamID, "invalid"); err != nil {
				t.Fatalf("FailPendingTask: %v", err)
			}
		} else {
			if err := f.teams.ClaimTask(f.ctx, a.ID, f.memberID, f.teamID); err != nil {
				t.Fatalf("ClaimTask: %v", err)
			}
			if err := f.teams.FailTask(f.ctx, a.ID, f.teamID, "boom"); err != nil {
				t.Fatalf("FailTask: %v", err)
			}
		}

		got, err := f.teams.GetTask(f.ctx, b.ID)
		if err != nil {
			t.Fatalf("GetTask B: %v", err)
		}
		if got.Status != store.TeamTaskStatusBlocked || len(got.BlockedBy) != 1 {
			t.Errorf("pending=%v: B = %q %v, want blocked [A]", pending, got.Status, got.BlockedBy)
		}
		fixed, err := f.teams.FixOrphanedBlockedTasks(f.ctx)
		if err != nil {
			t.Fatalf("FixOrphanedBlockedTasks: %v", err)
		}
		for _, x := range fixed {
			if x.ID == b.ID {
				t.Errorf("pending=%v: FixOrphanedBlockedTasks mở khóa B dù A failed", pending)
			}
		}
	}
}

func TestTeamTaskSQLite_QueuedBehindBusyOwnerNotStale(t *testing.T) {
	f := newDepFixture(t)
	running := f.create(t, "running")
	queued := f.create(t, "queued")
	idle := f.create(t, "idle")
	if err := f.teams.ClaimTask(f.ctx, running.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	old := time.Now().Add(-3 * time.Hour)
	mustExec(t, f.db, `UPDATE team_tasks SET owner_agent_id = ?, updated_at = ? WHERE id = ?`, f.memberID, old, queued.ID)
	mustExec(t, f.db, `UPDATE team_tasks SET updated_at = ? WHERE id = ?`, old, idle.ID)

	if _, err := f.teams.MarkAllStaleTasks(f.ctx, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("MarkAllStaleTasks: %v", err)
	}
	if got, _ := f.teams.GetTask(f.ctx, queued.ID); got.Status != store.TeamTaskStatusPending {
		t.Errorf("queued status = %q, want pending", got.Status)
	}
	if got, _ := f.teams.GetTask(f.ctx, idle.ID); got.Status != store.TeamTaskStatusStale {
		t.Errorf("idle status = %q, want stale", got.Status)
	}
}

// Recover chỉ trả về task thật sự vừa được đưa về pending, không gồm task
// pending khác vừa được tạo.
func TestTeamTaskSQLite_RecoverReturnsOnlyRecoveredRows(t *testing.T) {
	for _, force := range []bool{false, true} {
		f := newDepFixture(t)
		stuck := f.create(t, "stuck")
		fresh := f.create(t, "fresh")
		if err := f.teams.ClaimTask(f.ctx, stuck.ID, f.memberID, f.teamID); err != nil {
			t.Fatalf("ClaimTask: %v", err)
		}
		mustExec(t, f.db, `UPDATE team_tasks SET lock_expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour), stuck.ID)

		var got []store.RecoveredTaskInfo
		var err error
		if force {
			got, err = f.teams.ForceRecoverAllTasks(f.ctx)
		} else {
			got, err = f.teams.RecoverAllStaleTasks(f.ctx)
		}
		if err != nil {
			t.Fatalf("force=%v: recover: %v", force, err)
		}
		if len(got) != 1 || got[0].ID != stuck.ID {
			t.Errorf("force=%v: recovered = %+v, want only %s (fresh %s không được tính)", force, got, stuck.ID, fresh.ID)
		}
	}
}

func TestTeamTaskSQLite_ListBusyTaskOwners(t *testing.T) {
	f := newDepFixture(t)
	running := f.create(t, "running")
	f.create(t, "pending")
	if err := f.teams.ClaimTask(f.ctx, running.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	owners, err := f.teams.ListBusyTaskOwners(f.ctx, f.teamID)
	if err != nil {
		t.Fatalf("ListBusyTaskOwners: %v", err)
	}
	if len(owners) != 1 || owners[0] != f.memberID {
		t.Errorf("owners = %v, want [%s]", owners, f.memberID)
	}
}

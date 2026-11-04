//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestMarkTaskDeliverablesTagsExistingAttachmentOnce(t *testing.T) {
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	tenantID, agentID, teamID, taskID := store.GenNewID(), store.GenNewID(), store.GenNewID(), store.GenNewID()
	mustExec(t, db, `INSERT INTO tenants (id, name, slug, status, settings, created_at, updated_at)
		VALUES (?, 'deliverables', 'deliverables', 'active', '{}', datetime('now'), datetime('now'))`, tenantID)
	mustExec(t, db, `INSERT INTO agents (id, agent_key, owner_id, model, tenant_id)
		VALUES (?, 'lead', 'owner', 'gpt', ?)`, agentID, tenantID)
	mustExec(t, db, `INSERT INTO agent_teams (id, name, lead_agent_id, created_by, tenant_id)
		VALUES (?, 'team-a', ?, 'owner', ?)`, teamID, agentID, tenantID)
	mustExec(t, db, `INSERT INTO team_tasks (id, team_id, subject, tenant_id) VALUES (?, ?, 'work', ?)`, taskID, teamID, tenantID)
	teams := NewSQLiteTeamStore(db)
	ctx := store.WithTenantID(context.Background(), tenantID)

	// create_image attached this file automatically during the run.
	auto := &store.TeamTaskAttachmentData{TaskID: taskID, TeamID: teamID, Path: "/ws/final.png", Metadata: json.RawMessage(`{"source":"auto"}`)}
	if err := teams.AttachFileToTask(ctx, auto); err != nil {
		t.Fatalf("AttachFileToTask: %v", err)
	}

	atts := []*store.TeamTaskAttachmentData{
		{TaskID: taskID, TeamID: teamID, Path: "/ws/final.png", FileSize: 7},
		{TaskID: taskID, TeamID: teamID, Path: "/ws/report.pdf", FileSize: 9},
	}
	if err := teams.MarkTaskDeliverables(ctx, atts); err != nil {
		t.Fatalf("MarkTaskDeliverables: %v", err)
	}

	got, err := teams.ListTaskAttachments(ctx, taskID)
	if err != nil {
		t.Fatalf("ListTaskAttachments: %v", err)
	}
	if len(got) != 2 || !got[0].IsDeliverable() || !got[1].IsDeliverable() {
		t.Fatalf("attachments = %+v", got)
	}
	var meta map[string]string
	if err := json.Unmarshal(got[0].Metadata, &meta); err != nil || meta["source"] != "auto" {
		t.Fatalf("existing metadata must merge, got %s", got[0].Metadata)
	}
	var count int
	if err := db.QueryRow(`SELECT attachment_count FROM team_tasks WHERE id = ?`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("attachment_count = %d, the re-tagged file must not count twice", count)
	}
}

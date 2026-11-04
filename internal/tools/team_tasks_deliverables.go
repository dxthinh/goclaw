package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// maxTaskDeliverables caps how many final files one task can declare.
const maxTaskDeliverables = 20

// resolveTaskDeliverables validates the paths a member declares as final files,
// with the same checks as send_file. All bad paths are reported at once so the
// model can fix them in one call.
func resolveTaskDeliverables(ctx context.Context, raw any) ([]*store.TeamTaskAttachmentData, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("deliverables must be an array of file paths")
	}
	if len(items) > maxTaskDeliverables {
		return nil, fmt.Errorf("too many deliverables: %d (limit %d)", len(items), maxTaskDeliverables)
	}

	workspace := ToolWorkspaceFromCtx(ctx)
	allowed := allowedWithTeamWorkspace(ctx, nil)
	seen := make(map[string]struct{}, len(items))
	atts := make([]*store.TeamTaskAttachmentData, 0, len(items))
	var problems []string
	for _, item := range items {
		path, _ := item.(string)
		if strings.TrimSpace(path) == "" {
			problems = append(problems, fmt.Sprintf("%v: not a file path", item))
			continue
		}
		att, err := resolveTaskDeliverable(ctx, path, workspace, allowed)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if _, duplicate := seen[att.Path]; duplicate {
			continue
		}
		seen[att.Path] = struct{}{}
		atts = append(atts, att)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid deliverables, task not completed:\n- %s", strings.Join(problems, "\n- "))
	}
	return atts, nil
}

func resolveTaskDeliverable(ctx context.Context, path, workspace string, allowed []string) (*store.TeamTaskAttachmentData, error) {
	resolved, err := resolvePathWithAllowed(path, workspace, effectiveRestrict(ctx, true), allowed)
	if err != nil {
		return nil, err
	}
	if err := checkDeniedPath(resolved, workspace, InternalDenyPaths); err != nil {
		return nil, err
	}
	if err := ValidateRegularFileForRead(resolved); err != nil {
		return nil, fmt.Errorf("not a regular file: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("file not found")
	}
	if fi.Size() == 0 {
		return nil, fmt.Errorf("file is empty")
	}
	return &store.TeamTaskAttachmentData{
		Path:     resolved,
		FileSize: fi.Size(),
		MimeType: mimeFromExt(filepath.Ext(resolved)),
	}, nil
}

// checkDeliverablesAllowed lets only the task's owner or its own team's lead declare
// files. Declared files go straight to the user, so nobody else may add files to a
// task, including one that has already completed.
func (t *TeamTasksTool) checkDeliverablesAllowed(ctx context.Context, team *store.TeamData, taskID, agentID uuid.UUID) error {
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil || task == nil {
		return fmt.Errorf("cannot verify task owner, deliverables not registered: %w", err)
	}
	if task.TeamID != team.ID {
		return fmt.Errorf("task %s does not belong to your team", taskID)
	}
	if agentID == team.LeadAgentID || (task.OwnerAgentID != nil && *task.OwnerAgentID == agentID) {
		return nil
	}
	return fmt.Errorf("only the task owner or the team lead can declare deliverables for task %s", taskID)
}

// registerDeliverables stores validated deliverables and returns a sentence for the
// tool result. A store failure is reported to the model without failing the call,
// because the task is already complete at this point.
func (t *TeamTasksTool) registerDeliverables(ctx context.Context, teamID, taskID, agentID uuid.UUID, atts []*store.TeamTaskAttachmentData) string {
	if len(atts) == 0 {
		return ""
	}
	chatID := OriginChatIDFromCtx(ctx)
	for _, att := range atts {
		att.TaskID = taskID
		att.TeamID = teamID
		att.ChatID = chatID
		att.CreatedByAgentID = &agentID
	}
	if err := t.manager.Store().MarkTaskDeliverables(ctx, atts); err != nil {
		slog.Warn("team_tasks: register deliverables failed", "task_id", taskID, "error", err)
		return " Warning: could not register deliverables, so they will not be sent automatically."
	}
	return fmt.Sprintf(" Deliverables registered: %d.", len(atts))
}

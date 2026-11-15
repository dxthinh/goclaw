package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type approvalTeamStore struct {
	store.TeamStore
	team  *store.TeamData
	tasks []store.TeamTaskData
}

func (s *approvalTeamStore) GetTeamForAgent(context.Context, uuid.UUID) (*store.TeamData, error) {
	return s.team, nil
}

func (s *approvalTeamStore) GetTaskByNumberInChat(_ context.Context, _ uuid.UUID, num int, channel, chatID string) (*store.TeamTaskData, error) {
	for i := range s.tasks {
		t := s.tasks[i]
		if t.TaskNumber == num && t.Channel == channel && t.ChatID == chatID {
			return &t, nil
		}
	}
	return nil, store.ErrTaskNotFound
}

type approvalAgentStore struct {
	store.AgentStore
	agent *store.AgentData
}

func (s *approvalAgentStore) GetByKey(_ context.Context, key string) (*store.AgentData, error) {
	if key != s.agent.AgentKey {
		return nil, errors.New("not found")
	}
	return s.agent, nil
}

type approvalPermStore struct {
	store.ConfigPermissionStore
	writers map[string]bool
	scopes  []string
}

func (s *approvalPermStore) CheckPermission(_ context.Context, _ uuid.UUID, scope, configType, userID string) (bool, error) {
	s.scopes = append(s.scopes, scope)
	return configType == store.ConfigTypeFileWriter && s.writers[userID], nil
}

type fakeTaskFeedback struct {
	enabled  map[string]bool
	approved []uuid.UUID
	changes  []string
}

func (f *fakeTaskFeedback) AnswerTask(context.Context, *store.TeamData, *store.TeamTaskData, string, uuid.UUID, tools.TaskActor) error {
	return nil
}
func (f *fakeTaskFeedback) RequestTaskChanges(_ context.Context, _ *store.TeamData, _ *store.TeamTaskData, reason string, _ tools.TaskActor) error {
	f.changes = append(f.changes, reason)
	return nil
}
func (f *fakeTaskFeedback) ApproveUserCheckpoint(_ context.Context, _ *store.TeamData, task *store.TeamTaskData, _ tools.TaskActor) error {
	f.approved = append(f.approved, task.ID)
	return nil
}
func (f *fakeTaskFeedback) ChannelTaskApprovalEnabled(name string) bool { return f.enabled[name] }

type approvalCase struct {
	deps *ConsumerDeps
	fb   *fakeTaskFeedback
	perm *approvalPermStore
	task store.TeamTaskData
}

// newApprovalCase: task #7 tạo từ nhóm Telegram "-100" bởi user 123456, đang
// chờ user duyệt. Giá trị origin_sender_id là đúng giá trị Telegram lưu lúc
// create (ID số, xem telegram/handlers.go).
func newApprovalCase() *approvalCase {
	agent := &store.AgentData{BaseModel: store.BaseModel{ID: uuid.New()}, AgentKey: "lead"}
	task := store.TeamTaskData{
		BaseModel: store.BaseModel{ID: uuid.New()}, TaskNumber: 7, Subject: "Report",
		Status: store.TeamTaskStatusInReview, Channel: "tg-bot", ChatID: "-100",
		Metadata: map[string]any{
			tools.TaskMetaCheckpointPending: tools.CheckpointPendingUser,
			tools.MetaOriginSenderID:        "123456",
		},
	}
	c := &approvalCase{
		fb:   &fakeTaskFeedback{enabled: map[string]bool{"tg-bot": true}},
		perm: &approvalPermStore{writers: map[string]bool{"777": true}},
		task: task,
	}
	c.deps = &ConsumerDeps{
		TeamStore:       &approvalTeamStore{team: &store.TeamData{BaseModel: store.BaseModel{ID: uuid.New()}}, tasks: []store.TeamTaskData{task}},
		AgentStore:      &approvalAgentStore{agent: agent},
		ConfigPermStore: c.perm,
		TaskFeedback:    c.fb,
	}
	return c
}

func approvalMsg(cmd, sender, chatID string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel: "tg-bot", ChatID: chatID, SenderID: sender, PeerKind: "group", AgentID: "lead",
		Metadata: map[string]string{"command": cmd, "task_number": "7", "text": "use 2026 data"},
	}
}

func TestTaskApprovalCommand_ValidApproveAndChanges(t *testing.T) {
	c := newApprovalCase()
	for _, sender := range []string{"123456", "123456|alice"} {
		processTaskApprovalCommand(context.Background(), approvalMsg(commandTaskApprove, sender, "-100"), commandTaskApprove, c.deps)
	}
	if len(c.fb.approved) != 2 || c.fb.approved[0] != c.task.ID {
		t.Fatalf("approved = %v, want task approved twice", c.fb.approved)
	}
	processTaskApprovalCommand(context.Background(), approvalMsg(commandTaskChanges, "123456", "-100"), commandTaskChanges, c.deps)
	if len(c.fb.changes) != 1 || c.fb.changes[0] != "use 2026 data" {
		t.Fatalf("changes = %v", c.fb.changes)
	}
}

// Forum group: lệnh gõ trong đúng topic gốc thì được nhận.
func TestTaskApprovalCommand_SameForumTopicAllowed(t *testing.T) {
	c := newApprovalCase()
	c.deps.TeamStore.(*approvalTeamStore).tasks[0].Metadata[tools.TaskMetaLocalKey] = "-100:topic:5"
	m := approvalMsg(commandTaskApprove, "123456", "-100")
	m.Metadata["local_key"] = "-100:topic:5"
	processTaskApprovalCommand(context.Background(), m, commandTaskApprove, c.deps)
	if len(c.fb.approved) != 1 {
		t.Fatal("command from the task's own topic must be accepted")
	}
}

func TestTaskApprovalCommand_GroupWriterAllowed(t *testing.T) {
	c := newApprovalCase()
	processTaskApprovalCommand(context.Background(), approvalMsg(commandTaskApprove, "777", "-100"), commandTaskApprove, c.deps)
	if len(c.fb.approved) != 1 {
		t.Fatal("file writer of the group must be able to approve")
	}
	if len(c.perm.scopes) != 1 || c.perm.scopes[0] != "group:tg-bot:-100" {
		t.Errorf("writer scope = %v", c.perm.scopes)
	}
}

func TestTaskApprovalCommand_Denied(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*approvalCase) bus.InboundMessage
	}{
		{"internal sender", func(c *approvalCase) bus.InboundMessage {
			return approvalMsg(commandTaskApprove, "system:escalation", "-100")
		}},
		{"other chat", func(c *approvalCase) bus.InboundMessage {
			return approvalMsg(commandTaskApprove, "123456", "-200")
		}},
		{"not requester and not writer", func(c *approvalCase) bus.InboundMessage {
			return approvalMsg(commandTaskApprove, "999", "-100")
		}},
		{"channel approval disabled", func(c *approvalCase) bus.InboundMessage {
			c.fb.enabled["tg-bot"] = false
			return approvalMsg(commandTaskApprove, "123456", "-100")
		}},
		{"waiting for lead, not user", func(c *approvalCase) bus.InboundMessage {
			c.deps.TeamStore.(*approvalTeamStore).tasks[0].Metadata = map[string]any{
				tools.TaskMetaCheckpointPending: tools.CheckpointPendingLead, tools.MetaOriginSenderID: "123456"}
			return approvalMsg(commandTaskApprove, "123456", "-100")
		}},
		{"task not in review", func(c *approvalCase) bus.InboundMessage {
			c.deps.TeamStore.(*approvalTeamStore).tasks[0].Status = store.TeamTaskStatusInProgress
			return approvalMsg(commandTaskApprove, "123456", "-100")
		}},
		{"other forum topic", func(c *approvalCase) bus.InboundMessage {
			c.deps.TeamStore.(*approvalTeamStore).tasks[0].Metadata[tools.TaskMetaLocalKey] = "-100:topic:5"
			m := approvalMsg(commandTaskApprove, "123456", "-100")
			m.Metadata["local_key"] = "-100:topic:9"
			return m
		}},
		{"topic task, command from general chat", func(c *approvalCase) bus.InboundMessage {
			c.deps.TeamStore.(*approvalTeamStore).tasks[0].Metadata[tools.TaskMetaLocalKey] = "-100:topic:5"
			return approvalMsg(commandTaskApprove, "123456", "-100")
		}},
		{"unknown task number", func(c *approvalCase) bus.InboundMessage {
			m := approvalMsg(commandTaskApprove, "123456", "-100")
			m.Metadata["task_number"] = "8"
			return m
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newApprovalCase()
			msg := tc.setup(c)
			processTaskApprovalCommand(context.Background(), msg, commandTaskApprove, c.deps)
			if len(c.fb.approved) != 0 || len(c.fb.changes) != 0 {
				t.Fatalf("command must be denied, got approved=%v changes=%v", c.fb.approved, c.fb.changes)
			}
		})
	}
}

func TestHandleTaskApprovalCommand_IgnoresOtherMessages(t *testing.T) {
	c := newApprovalCase()
	if handleTaskApprovalCommand(context.Background(), bus.InboundMessage{Metadata: map[string]string{"command": "stop"}}, c.deps) {
		t.Error("non-approval command must not be handled")
	}
	if !handleTaskApprovalCommand(context.Background(), approvalMsg(commandTaskApprove, "123456", "-100"), c.deps) {
		t.Error("approval command must be handled")
	}
}

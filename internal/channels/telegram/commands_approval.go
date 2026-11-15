package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// Lệnh duyệt kết quả task của team. Kênh chỉ phân tích cú pháp và phát lên bus;
// consumer của gateway kiểm tra quyền và xử lý trước agent, không qua LLM.
const (
	cmdTaskApprove = "task_approve"
	cmdTaskChanges = "task_changes"
)

const (
	approveUsage = "Usage: /approve <task number>"
	changesUsage = "Usage: /changes <task number> <reason>"
)

// parseTaskApprovalArgs đọc tham số của /approve hoặc /changes từ toàn bộ tin
// nhắn. Trả số task, lý do (chỉ /changes) và thông báo cú pháp khi sai.
func parseTaskApprovalArgs(cmd, text string) (int, string, string) {
	usage := approveUsage
	if cmd == "/changes" {
		usage = changesUsage
	}
	parts := strings.SplitN(strings.TrimSpace(text), " ", 2)
	if len(parts) < 2 {
		return 0, "", usage
	}
	rest := strings.TrimSpace(parts[1])
	numStr, reason, _ := strings.Cut(rest, " ")
	n, err := strconv.Atoi(strings.TrimPrefix(numStr, "#"))
	if err != nil || n <= 0 {
		return 0, "", usage
	}
	reason = strings.TrimSpace(reason)
	if cmd == "/changes" && reason == "" {
		return 0, "", usage
	}
	return n, reason, ""
}

// handleTaskApprovalCommand xử lý /approve và /changes: kiểm tra kênh đã bật
// duyệt, phân tích tham số rồi phát InboundMessage có Metadata["command"]
// theo mẫu /stop. Câu trả lời cho người gửi do consumer gửi sau khi xử lý.
func (c *Channel) handleTaskApprovalCommand(ctx context.Context, cmd, text, chatIDStr, localKey, senderID string,
	chatIDObj telego.ChatID, isGroup, isForum bool, messageThreadID int, setThread func(*telego.SendMessageParams)) {
	reply := func(s string) {
		msg := tu.Message(chatIDObj, s)
		setThread(msg)
		c.bot.SendMessage(ctx, msg)
	}
	if !c.TaskApprovalEnabled() {
		reply("Task approval by command is not enabled for this bot.")
		return
	}
	num, reason, usage := parseTaskApprovalArgs(cmd, text)
	if usage != "" {
		reply(usage)
		return
	}
	command := cmdTaskApprove
	if cmd == "/changes" {
		command = cmdTaskChanges
	}
	peerKind := "direct"
	if isGroup {
		peerKind = "group"
	}
	c.Bus().PublishInbound(bus.InboundMessage{
		Channel:  c.Name(),
		SenderID: senderID,
		ChatID:   chatIDStr,
		Content:  strings.TrimSpace(text),
		PeerKind: peerKind,
		AgentID:  c.AgentID(),
		UserID:   strings.SplitN(senderID, "|", 2)[0],
		TenantID: c.TenantID(),
		Metadata: map[string]string{
			"command":           command,
			"task_number":       strconv.Itoa(num),
			"text":              reason,
			"local_key":         localKey,
			"is_forum":          fmt.Sprintf("%t", isForum),
			"message_thread_id": fmt.Sprintf("%d", messageThreadID),
		},
	})
}

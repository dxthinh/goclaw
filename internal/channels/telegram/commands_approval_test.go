package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestParseTaskApprovalArgs(t *testing.T) {
	cases := []struct {
		cmd, text string
		num       int
		reason    string
		wantUsage bool
	}{
		{"/approve", "/approve 12", 12, "", false},
		{"/approve", "/approve@my_bot #12", 12, "", false},
		{"/approve", "/approve", 0, "", true},
		{"/approve", "/approve abc", 0, "", true},
		{"/approve", "/approve 0", 0, "", true},
		{"/changes", "/changes 5 add the 2026 numbers", 5, "add the 2026 numbers", false},
		{"/changes", "/changes 5", 0, "", true},
		{"/changes", "/changes 5    ", 0, "", true},
		{"/changes", "/changes x reason", 0, "", true},
	}
	for _, tc := range cases {
		num, reason, usage := parseTaskApprovalArgs(tc.cmd, tc.text)
		if (usage != "") != tc.wantUsage || num != tc.num || reason != tc.reason {
			t.Errorf("parse(%q) = %d %q usage=%q, want %d %q usage=%v", tc.text, num, reason, usage, tc.num, tc.reason, tc.wantUsage)
		}
	}
}

func TestTaskStatusIcon_ReviewAndAwaitingInput(t *testing.T) {
	if taskStatusIcon("in_review") == taskStatusIcon("pending") || taskStatusIcon("awaiting_input") == taskStatusIcon("pending") {
		t.Error("in_review and awaiting_input need their own icons")
	}
}

// fakeGroupPairing: chỉ trả paired cho các sender trong danh sách.
type fakeGroupPairing struct {
	store.PairingStore
	paired map[string]bool
	err    error
}

func (f *fakeGroupPairing) IsPaired(_ context.Context, senderID, _ string) (bool, error) {
	return f.paired[senderID], f.err
}

// Lệnh duyệt phải qua gate pairing của nhóm như tin nhắn thường.
func TestGroupPairingAllowed(t *testing.T) {
	newCh := func(policy string, ps store.PairingStore) *Channel {
		ch := &Channel{BaseChannel: channels.NewBaseChannel("tg", nil, nil), config: config.TelegramConfig{GroupPolicy: policy}}
		if ps != nil {
			ch.SetPairingService(ps)
		}
		return ch
	}
	ctx := context.Background()
	if !newCh("open", &fakeGroupPairing{}).groupPairingAllowed(ctx, "-100", 0) {
		t.Error("open policy must allow")
	}
	if newCh("pairing", &fakeGroupPairing{}).groupPairingAllowed(ctx, "-100", 0) {
		t.Error("unpaired group must be denied")
	}
	if newCh("pairing", &fakeGroupPairing{err: errors.New("db down")}).groupPairingAllowed(ctx, "-100", 0) {
		t.Error("pairing check error must fail closed")
	}
	ch := newCh("pairing", &fakeGroupPairing{paired: map[string]bool{"group:-100": true}})
	if !ch.groupPairingAllowed(ctx, "-100", 0) || !ch.IsGroupApproved("-100") {
		t.Error("paired group must be allowed and cached")
	}
}

package telegram

import "testing"

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

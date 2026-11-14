//go:build !sqliteonly

package cmd

import "testing"

func TestTraceRepairRequiresBackupAndOfflineBeforeConnecting(t *testing.T) {
	for _, extra := range [][]string{nil, {"--offline"}, {"--backup", "unused.sql"}} {
		cmd := tracesRepairSnapshotsCmd()
		args := []string{"--from", "2026-11-02T00:00:00Z", "--to", "2026-11-03T00:00:00Z", "--apply"}
		cmd.SetArgs(append(args, extra...))
		if err := cmd.Execute(); err == nil || err.Error() != "--apply requires --offline acknowledgement and a new --backup path" {
			t.Fatalf("expected missing safeguards before database access, got %v", err)
		}
	}
}

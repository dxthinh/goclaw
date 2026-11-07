package integration

import (
	"context"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

func TestSnapshotRepairRejectsUnsafeBoundsBeforeDatabaseAccess(t *testing.T) {
	hour := time.Now().UTC().Truncate(time.Hour)
	for name, bounds := range map[string][2]time.Time{
		"pruned":   {hour.Add(-8 * 24 * time.Hour), hour.Add(-7 * 24 * time.Hour)},
		"boundary": {hour.Add(-7 * 24 * time.Hour), hour.Add(-7*24*time.Hour + time.Hour)},
		"current":  {hour, hour.Add(time.Hour)},
		"partial":  {hour.Add(-time.Hour + time.Minute), hour},
		"empty":    {hour, hour},
		"reversed": {hour, hour.Add(-time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			_, err := tracing.RepairSnapshots(context.Background(), nil, bounds[0], bounds[1], func(context.Context) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("unsafe range accepted or backup called: err=%v called=%t", err, called)
			}
		})
	}
}

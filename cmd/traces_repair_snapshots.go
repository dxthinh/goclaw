//go:build !sqliteonly

package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/backup"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
	"github.com/spf13/cobra"
)

func addTraceRepairSnapshotsCommand(cmd *cobra.Command) {
	cmd.AddCommand(tracesRepairSnapshotsCmd())
}

func tracesRepairSnapshotsCmd() *cobra.Command {
	var fromText, toText, backupPath string
	var apply, offline bool
	cmd := &cobra.Command{
		Use:   "repair-snapshots",
		Short: "Preview or repair retained PostgreSQL usage snapshots (offline, backup first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := time.Parse(time.RFC3339, fromText)
			if err != nil {
				return fmt.Errorf("%s: %w", i18n.T("", i18n.MsgSnapshotRepairFrom), err)
			}
			to, err := time.Parse(time.RFC3339, toText)
			if err != nil {
				return fmt.Errorf("%s: %w", i18n.T("", i18n.MsgSnapshotRepairTo), err)
			}
			if apply && (!offline || backupPath == "") {
				return errors.New(i18n.T("", i18n.MsgSnapshotRepairApply))
			}
			dsn, err := resolveDSN()
			if err != nil {
				return err
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				return err
			}
			defer db.Close()
			var createBackup func(context.Context) error
			if apply {
				createBackup = func(ctx context.Context) error {
					// Exclusive creation prevents overwriting a previous backup.
					file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
					if err != nil {
						return err
					}
					if err := backup.DumpDatabase(ctx, dsn, file); err != nil {
						file.Close()
						return err // Retain partial file for diagnosis; never apply.
					}
					if err := file.Sync(); err != nil {
						file.Close()
						return err
					}
					return file.Close()
				}
			}
			count, err := tracing.RepairSnapshots(cmd.Context(), db, from.UTC(), to.UTC(), createBackup)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "apply=%t from=%s to=%s source_traces=%d\n",
				apply, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), count)
			return err
		},
	}
	cmd.Flags().StringVar(&fromText, "from", "", "inclusive UTC hour (RFC3339)")
	cmd.Flags().StringVar(&toText, "to", "", "exclusive UTC hour (RFC3339)")
	cmd.Flags().BoolVar(&apply, "apply", false, "write snapshots after successful database backup")
	cmd.Flags().BoolVar(&offline, "offline", false, "acknowledge gateway and other writers are stopped; retained source has not been manually pruned")
	cmd.Flags().StringVar(&backupPath, "backup", "", "new pg_dump SQL backup path required for writes")
	return cmd
}

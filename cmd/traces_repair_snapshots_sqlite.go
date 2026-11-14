//go:build sqliteonly

package cmd

import "github.com/spf13/cobra"

// Historical snapshot repair is a PostgreSQL operator command.
func addTraceRepairSnapshotsCommand(_ *cobra.Command) {}

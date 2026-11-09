// Copyright 2026 Paul Taramona and contributors. Licensed under Apache-2.0. See LICENSE.
package cli

import "github.com/spf13/cobra"

// PATCH(novel1219-restored-commands): the four commands the public library entry serves and the 4.34.2 reprint
// dropped (Greptile PR #2450 round 2 P1) are attached to their generated parent groups through the novel-command
// hook, so a reprint that regenerates reports.go, dialer.go and automations.go keeps them.
func init() {
	registerNovelCommand(func(root *cobra.Command, flags *rootFlags) {
		attach := func(parent string, child *cobra.Command) {
			for _, cmd := range root.Commands() {
				if cmd.Name() == parent {
					addNovelCommandIfAbsent(cmd, child)
					return
				}
			}
		}
		attach("reports", newNovelReportsCompareCmd(flags))
		attach("reports", newNovelReportsScorecardCmd(flags))
		attach("dialer", newNovelDialerCoverageCmd(flags))
		attach("automations", newNovelAutomationsHoursAuditCmd(flags))
	})
}

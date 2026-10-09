package main

import (
	"fmt"

	"github.com/zulucmd/zulu/v2"
)

func newVersionCmd(version, commit, date string) *zulu.Command {
	return &zulu.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  zulu.NoArgs,
		RunE: func(cmd *zulu.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "geocoder-proxy %s (commit %s, built %s)\n", version, commit, date)
			return nil
		},
	}
}

package main

import (
	"github.com/zulucmd/zulu/v2"
)

func newRootCmd(version, commit, date string) *zulu.Command {
	root := &zulu.Command{
		Use:   "geocoder-proxy",
		Short: "Geocoding service fronting multiple upstream providers behind one Photon-compatible API",
		// Errors are printed by main; usage belongs to --help, not to every failure.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd(version, commit, date))
	return root
}

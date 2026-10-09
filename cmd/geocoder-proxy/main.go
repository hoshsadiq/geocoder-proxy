// geocoder-proxy is a geocoding service that fronts multiple upstream
// providers behind one Photon-compatible API, built so Dawarich can point
// PHOTON_API_HOST at it unchanged.
package main

import (
	"fmt"
	"os"
)

// Build information, injected via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := newRootCmd(version, commit, date).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

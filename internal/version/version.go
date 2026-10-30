// Package version holds the build information, set by the release pipeline with -ldflags -X.
package version

import (
	"fmt"
	"runtime"
)

var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Repo is the project URL, which appears in the User-Agent and on the status pages.
const Repo = "https://github.com/openlinkos/status"

// String is the output of the version subcommand.
func String() string {
	s := "linkos-status " + Version
	if Commit != "" {
		s += " (" + Commit
		if Date != "" {
			s += ", " + Date
		}
		s += ")"
	}
	return fmt.Sprintf("%s %s %s/%s", s, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// UserAgent is the User-Agent of the requests the service makes (Alertmanager, Feishu, Pushover, probes).
func UserAgent() string { return "linkos-status/" + Version + " (+" + Repo + ")" }

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/oncall"
)

func checkDrill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("beacon-oncall check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var check oncall.DrillCheck
	fs.StringVar(&check.URL, "url", "", "Beacon's external base URL")
	fs.StringVar(&check.Owner, "owner", "", "username that owns the on-call agent")
	fs.StringVar(&check.Agent, "agent", "", "Beacon agent name")
	fs.StringVar(&check.Machine, "machine", "", "enrolled machine selected for the drill")
	fs.StringVar(&check.Reporter, "reporter", "", "standalone token name in the hook's reporter field")
	agentFile := fs.String("agent-token-file", "", "file used by the on-call agent")
	reportFile := fs.String("report-token-file", "", "file used by the harness to report execution")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || check.URL == "" || check.Owner == "" || check.Agent == "" || check.Machine == "" ||
		check.Reporter == "" || *agentFile == "" || *reportFile == "" {
		fmt.Fprintln(stderr, "check requires -url, -owner, -agent, -machine, -reporter, -agent-token-file and -report-token-file")
		return 2
	}

	agent, err := os.ReadFile(*agentFile)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read the agent token file")
		return 1
	}
	reporter, err := os.ReadFile(*reportFile)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read the report token file")
		return 1
	}
	check.AgentToken = strings.TrimSpace(string(agent))
	check.ReportToken = strings.TrimSpace(string(reporter))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := oncall.CheckDrill(ctx, check); err != nil {
		fmt.Fprintln(stderr, "drill check failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Agent %q / %q is limited to enrolled machine %q. Reporter token %q has alerts:report.\n",
		check.Owner, check.Agent, check.Machine, check.Reporter)
	return 0
}

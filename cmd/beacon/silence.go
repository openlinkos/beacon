package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/ctl"
)

const silenceUsage = `silence create -matchers 'alertname=ServiceDown,service=example.com' -duration 4h -comment TEXT
silence create -alert GROUP_ID -duration 4h -comment TEXT
silence list
silence expire ID`

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// parseMatchers reads "alertname=ServiceDown,service=example.com" into labels an alert must all have.
func parseMatchers(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !labelName.MatchString(k) || v == "" {
			return nil, fmt.Errorf("matcher %q: write it as label=value, such as alertname=ServiceDown", part)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("matcher %q: %s is given twice", part, k)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, errors.New("no matchers: write them as alertname=ServiceDown,service=example.com")
	}
	return out, nil
}

// matchersText writes a silence's matchers the way -matchers takes them.
func matchersText(ms []alertmanager.Matcher) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		op := "="
		switch {
		case m.IsRegex && m.IsEqual:
			op = "=~"
		case m.IsRegex:
			op = "!~"
		case !m.IsEqual:
			op = "!="
		}
		parts = append(parts, m.Name+op+m.Value)
	}
	return strings.Join(parts, ",")
}

// silenceCmd handles `silence create`, `silence list` and `silence expire ID`, through the running
// service, which audits them as the command line's user and echoes them to the alert chat.
func silenceCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, silenceUsage)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("silence create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		matchers := fs.String("matchers", "", "labels the alerts must all have, such as alertname=ServiceDown,service=example.com")
		alert := fs.String("alert", "", "an alert group's id instead of matchers, as the API and silence_alert take it")
		duration := fs.String("duration", "", "how long, such as 30m or 4h (at most 24h)")
		comment := fs.String("comment", "", "why: kept with the silence and shown in the alert chat")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 || (*matchers == "") == (*alert == "") || *duration == "" || strings.TrimSpace(*comment) == "" {
			fmt.Fprintln(stderr, silenceUsage)
			return 2
		}
		req := ctl.SilenceRequest{Alert: *alert, Duration: *duration, Comment: *comment}
		if *matchers != "" {
			m, err := parseMatchers(*matchers)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			req.Matchers = m
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		out, err := c.CreateSilence(ctx, req)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "silence %s until %s: %s\n", out.Silence.ID, out.Silence.EndsAt.Local().Format(time.DateTime), matchersText(out.Silence.Matchers))
		if out.Group != nil {
			fmt.Fprintf(stdout, "alert group %s (%s) is silenced and acknowledged\n", out.Group.ID, out.Group.Title)
		}
		return 0
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(stderr, silenceUsage)
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		list, err := c.ListSilences(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATE\tMATCHERS\tENDS\tBY\tCOMMENT")
		for _, s := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Status.State, matchersText(s.Matchers), s.EndsAt.Local().Format(time.DateTime), s.CreatedBy, s.Comment)
		}
		_ = tw.Flush()
		return 0
	case "expire":
		if len(args) != 2 {
			fmt.Fprintln(stderr, silenceUsage)
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		if err := c.ExpireSilence(ctx, args[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "silence %s ended\n", args[1])
		return 0
	}
	fmt.Fprintln(stderr, silenceUsage)
	return 2
}

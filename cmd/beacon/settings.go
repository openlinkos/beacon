package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/settings"
)

const settingsUsage = "settings [get [KEY]] | settings set KEY VALUE [-scope system] [-target NAME] [-reason TEXT] | settings reset KEY [-scope system] [-target NAME] [-reason TEXT] | settings history [-limit 20] | settings revert VERSION [-note TEXT]"

// settingsCmd reads and changes the settings the secrets work runs by, through the running
// service's socket, as `beacon settings …`. Values are JSON, as they are stored: 50, "30d",
// ["rules","agents"], an object.
func settingsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, settingsUsage)
		return 2
	}
	fs := flag.NewFlagSet("settings "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "system", "where the value sits: system, collection, tag, entitlement or request")
	target := fs.String("target", "", "what the scope names: the collection, tag, entitlement or request")
	reason := fs.String("reason", "", "why: kept with the change and shown in the alert chat")
	note := fs.String("note", "", "why")
	limit := fs.Int("limit", 20, "how many changes to list")
	pos, ok := positional(fs, args[1:])
	if !ok {
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "get":
		if len(pos) > 1 {
			fmt.Fprintln(stderr, settingsUsage)
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		if len(pos) == 0 {
			out, err := c.ListSettings(ctx)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tVALUE\tFROM\tWHO\tWHEN")
			for _, s := range out.Settings {
				who := ""
				if s.Actor != "" {
					who = s.Actor + " (" + s.Via + ")"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Key, compactJSON(s.Value), s.From, who, s.At)
			}
			_ = tw.Flush()
			for _, o := range out.Overrides {
				fmt.Fprintf(stdout, "override\t%s\t%s\t%s\t%s\n", o.Scope, o.Target, o.Key, compactJSON(o.Value))
			}
			for _, b := range out.BadRows {
				fmt.Fprintf(stdout, "warning: a row failed its signature and is served as the last signed value: %s\n", b)
			}
			return 0
		}
		res, err := c.GetSetting(ctx, pos[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\t%s\n", pos[0], compactJSON(res.Value))
		fmt.Fprintf(stdout, "from %s (default %s)\n", res.From, compactJSON(resDefault(pos[0])))
		return 0
	case "set":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, `settings set KEY VALUE: the value is JSON, as it is stored: 50, "30d", ["rules","agents"], {"keep":"365d","min_versions":10}`)
			return 2
		}
		if !json.Valid([]byte(pos[1])) {
			fmt.Fprintln(stderr, "the value is not JSON")
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		ch, err := c.SetSetting(ctx, ctl.SetSettingRequest{Key: pos[0], Value: []byte(pos[1]),
			Scope: *scope, Target: *target, Reason: *reason})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printSettingChange(stdout, ch)
		return 0
	case "reset":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, settingsUsage)
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		ch, err := c.ResetSetting(ctx, ctl.ResetSettingRequest{Key: pos[0], Scope: *scope, Target: *target, Reason: *reason})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printSettingChange(stdout, ch)
		return 0
	case "history":
		if len(pos) != 0 {
			fmt.Fprintln(stderr, settingsUsage)
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		list, err := c.SettingsHistory(ctx, *limit)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tTIME\tWHO\tVIA\tACTION\tSETTING\tNOTE")
		for _, ch := range list {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", ch.Version, ch.Time.Local().Format(time.DateTime),
				ch.Actor, ch.Via, ch.Action, ch.Key+whereOf(ch), noteOf(ch))
		}
		_ = tw.Flush()
		return 0
	case "revert":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, settingsUsage)
			return 2
		}
		v, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil || v < 1 {
			fmt.Fprintln(stderr, "settings revert VERSION: the version is a whole number, as settings history lists it")
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		ch, err := c.RevertSetting(ctx, ctl.RevertSettingRequest{Version: v, Reason: *note})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printSettingChange(stdout, ch)
		return 0
	}
	fmt.Fprintln(stderr, settingsUsage)
	return 2
}

// resDefault is a key's registered default, for the line under a setting's value.
func resDefault(key string) json.RawMessage {
	if d, ok := settings.Get(key); ok {
		return d.Default
	}
	return json.RawMessage("null")
}

func whereOf(ch settings.Change) string {
	if ch.Target != "" {
		return " at " + ch.Scope + " " + ch.Target
	}
	return " at " + ch.Scope
}

func noteOf(ch settings.Change) string {
	if ch.Reverts != 0 {
		return fmt.Sprintf("undoing %d; %s", ch.Reverts, ch.Reason)
	}
	return ch.Reason
}

func printSettingChange(w io.Writer, ch settings.Change) {
	if ch.Unchanged {
		fmt.Fprintf(w, "nothing to change: version %d holds this already\n", ch.Version)
		return
	}
	fmt.Fprintf(w, "version %d: %s %s%s\n", ch.Version, actionWord(ch.Action), ch.Key, whereOf(ch))
	if ch.Reverts != 0 {
		fmt.Fprintf(w, "  undoes version %d\n", ch.Reverts)
	}
	if !absentJSON(ch.Before) {
		fmt.Fprintf(w, "  was %s\n", compactJSON(ch.Before))
	}
	if !absentJSON(ch.After) {
		fmt.Fprintf(w, "  now %s\n", compactJSON(ch.After))
	}
	if ch.Reason != "" {
		fmt.Fprintf(w, "  why: %s\n", ch.Reason)
	}
}

func actionWord(action string) string {
	switch action {
	case "reset":
		return "reset"
	case "revert":
		return "reverted"
	}
	return "set"
}

func absentJSON(v json.RawMessage) bool { return len(v) == 0 || string(v) == "null" }

func compactJSON(v json.RawMessage) string {
	s := string(v)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

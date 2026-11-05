package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/settings"
)

// The console's settings page: every setting with the value in force, the registered default,
// where the value was overridden, who changed it and when; a change form and a reset for whoever
// holds secrets:admin; the history with revert. Rows that failed their signature are shown as a
// warning: they are served as the last signed value, and were reported to the log, the audit log
// and the alert chat.

type settingsLabels struct {
	Group, Key, What, Value, Default, From, Who, When, Reason    string
	Change, ChangeValue, ChangeWhy, Reset, ResetHint             string
	History, Version, Time, Via, Action, Revert, RevertWhy       string
	Empty, BadRows, BadRowsHint, Overrides, Scope, Target, Level string
	LevelHint, Requests, Review, Use, Retention, ImportX, Backup string
	Storage, None, OverriddenAt                                  string
}

func (s *Server) settingsLabels() settingsLabels {
	return settingsLabels{
		Group: s.t("Setting"), Key: s.t("Key"), What: s.t("What it turns"), Value: s.t("Value"),
		Default: s.t("Default"), From: s.t("In force from"), Who: s.t("Who"), When: s.t("When"), Reason: s.t("Why"),
		Change: s.t("Change"), ChangeValue: s.t("New value (JSON)"), ChangeWhy: s.t("Why change it"),
		Reset: s.t("Reset"), ResetHint: s.t("Resetting takes the override away, so the level above (or the default) answers again."),
		History: s.t("History"), Version: s.t("Version"), Time: s.t("Time"), Via: s.t("Via"), Action: s.t("Action"),
		Revert: s.t("Revert this change"), RevertWhy: s.t("Why revert it"),
		Empty: s.t("None"), BadRows: s.t("Rows that failed their signature"), Overrides: s.t("Overrides at other levels"),
		BadRowsHint: s.t("A row that fails its signature is served as the last signed value (never a looser default) and reported to the log, the audit log and the alert chat."),
		Scope:       s.t("Scope"), Target: s.t("Target"), Level: s.t("Level"), LevelHint: s.t("The more specific level wins; tags take the stricter value; a request is clamped to the bound the level above gives."),
		Requests: s.t("Requests and grants"), Review: s.t("Review"), Use: s.t("Use, rate limits and anomalies"),
		Retention: s.t("Retention"), ImportX: s.t("Import"), Backup: s.t("Backup"), Storage: s.t("Storage"), None: s.t("None"),
		OverriddenAt: s.t("overridden"),
	}
}

func (s *Server) groupLabel(g string) string {
	switch g {
	case "requests":
		return s.t("Requests and grants")
	case "review":
		return s.t("Review")
	case "use":
		return s.t("Use, rate limits and anomalies")
	case "retention":
		return s.t("Retention")
	case "import":
		return s.t("Import")
	case "backup":
		return s.t("Backup")
	}
	return s.t("Storage")
}

type settingRow struct {
	Key, What, Value, Default, From, Who, When, Why string
	Overridden                                      bool
	Set, Reset                                      string // where the forms post; "" leaves them out
}

type overrideRow struct {
	Scope, Target, Key, Value, Who, When string
}

type settingsChangeRow struct {
	Version, Time, Who, Via, Action, Setting, Why, Revert string
	Reverts                                               int64
}

type settingsPage struct {
	shell
	L         settingsLabels
	Groups    []settingsGroup
	Overrides []overrideRow
	Changes   []settingsChangeRow
	BadRows   []string
	CanChange bool
}

type settingsGroup struct {
	Title string
	Rows  []settingRow
}

var settingsTmpl = parse("settings.html")

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := settingsPage{shell: s.shellFor(r, v, "settings", s.t("System settings")), L: s.settingsLabels()}
	p.CanChange = v.Principal.Has("secrets:admin")
	if s.Settings == nil {
		s.render(w, settingsTmpl, p)
		return
	}
	listing := s.Settings.List()
	titles := map[string]string{}
	for _, g := range settings.Groups {
		titles[g] = s.groupLabel(g)
		group := settingsGroup{Title: titles[g]}
		for _, sv := range listing.Settings {
			if sv.Group != g {
				continue
			}
			row := settingRow{Key: sv.Key, What: sv.What, Value: compactJSON(sv.Value), Default: compactJSON(sv.Default),
				From: sv.From, Overridden: sv.From != "default"}
			if sv.Actor != "" {
				row.Who = sv.Actor + " (" + sv.Via + ")"
				if t, err := time.Parse(time.RFC3339, sv.At); err == nil {
					row.When = s.clock(t)
				}
				row.Why = sv.Reason
			}
			if p.CanChange {
				row.Set = consolePath("/settings/" + sv.Key + "/set")
				if row.Overridden {
					row.Reset = consolePath("/settings/" + sv.Key + "/reset")
				}
			}
			group.Rows = append(group.Rows, row)
		}
		p.Groups = append(p.Groups, group)
	}
	if p.CanChange {
		for _, o := range listing.Overrides {
			when := ""
			if t, err := time.Parse(time.RFC3339, o.At); err == nil {
				when = s.clock(t)
			}
			p.Overrides = append(p.Overrides, overrideRow{Scope: o.Scope, Target: o.Target, Key: o.Key,
				Value: compactJSON(o.Value), Who: o.Actor + " (" + o.Via + ")", When: when})
		}
		p.BadRows = listing.BadRows
		for _, ch := range s.Settings.History("", "", "", 0, 50) {
			row := settingsChangeRow{Version: strconv.FormatInt(ch.Version, 10), Time: s.clock(ch.Time),
				Who: ch.Actor, Via: ch.Via, Action: ch.Action, Why: ch.Reason, Reverts: ch.Reverts,
				Revert: consolePath("/settings/history/" + strconv.FormatInt(ch.Version, 10) + "/revert")}
			where := ch.Scope
			if ch.Target != "" {
				where += " " + ch.Target
			}
			row.Setting = ch.Key + " @ " + where
			p.Changes = append(p.Changes, row)
		}
	}
	s.render(w, settingsTmpl, p)
}

func compactJSON(v json.RawMessage) string {
	out := string(v)
	if len(out) > 80 {
		out = out[:77] + "..."
	}
	return out
}

// setSetting is the change form: the value as JSON, and why.
func (s *Server) setSetting(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Settings == nil {
		s.missing(w, r, v)
		return
	}
	key := r.PathValue("key")
	value := strings.TrimSpace(r.PostForm.Get("value"))
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		back(w, r, consolePath("/settings"), "error", "setting_invalid")
		return
	}
	_, err := s.Settings.Set(r.Context(), settings.Actor{Name: v.name(), Via: "console"}, settings.ScopeSystem, "", key, raw, r.PostForm.Get("reason"), 0)
	switch {
	case errors.Is(err, settings.ErrUnknown), errors.Is(err, settings.ErrNotFound):
		s.missing(w, r, v)
	case err != nil:
		s.failed(w, r, consolePath("/settings"), "change", err)
	default:
		back(w, r, consolePath("/settings"), "done", "changed")
	}
}

// resetSetting takes the system-level override away, so the default answers again.
func (s *Server) resetSetting(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Settings == nil {
		s.missing(w, r, v)
		return
	}
	_, err := s.Settings.Reset(r.Context(), settings.Actor{Name: v.name(), Via: "console"}, settings.ScopeSystem, "", r.PathValue("key"), r.PostForm.Get("reason"), 0)
	switch {
	case errors.Is(err, settings.ErrUnknown), errors.Is(err, settings.ErrNotFound):
		s.missing(w, r, v)
	case err != nil:
		s.failed(w, r, consolePath("/settings"), "reset", err)
	default:
		back(w, r, consolePath("/settings"), "done", "reset")
	}
}

// revertSetting undoes one change, as a new change.
func (s *Server) revertSetting(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Settings == nil {
		s.missing(w, r, v)
		return
	}
	n, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || n < 1 {
		s.missing(w, r, v)
		return
	}
	_, err = s.Settings.Revert(r.Context(), settings.Actor{Name: v.name(), Via: "console"}, n, r.PostForm.Get("reason"))
	switch {
	case errors.Is(err, settings.ErrNotFound), errors.Is(err, settings.ErrUnknown):
		s.missing(w, r, v)
	case err != nil:
		s.failed(w, r, consolePath("/settings"), "revert", err)
	default:
		back(w, r, consolePath("/settings"), "done", "reverted")
	}
}

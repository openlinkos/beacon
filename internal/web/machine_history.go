package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/sshgate"
)

type machineHistoryLabels struct {
	Version, Change, Who, Time, Account, Critical, Note, Restore, Older, Empty, Scope, Current string
}

type machineRevisionRow struct {
	Version                                          int64
	Current                                          bool
	Who, Time, Change, User, Critical, Note, Restore string
}

type machineHistoryPage struct {
	shell
	L           machineHistoryLabels
	Rows        []machineRevisionRow
	Current     int64
	CanRestore  bool
	Older, Post string
}

var machineHistoryTmpl = parse("machine_history.html")

func (s *Server) machineHistory(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	const limit = 20
	history, err := s.SSH.MachineHistory(name, max(0, before), limit+1)
	if errors.Is(err, sshgate.ErrNoMachine) || errors.Is(err, sshgate.ErrDisabled) {
		s.missing(w, r, v)
		return
	}
	if err != nil {
		s.log().Error("machine history unavailable", "machine", name, "err", err)
		s.message(w, r, v, http.StatusInternalServerError, s.t("Settings history"), s.t("The settings history could not be loaded."))
		return
	}
	path := machinePath(name)
	p := machineHistoryPage{
		shell: s.shellFor(r, v, "machines", s.t("Settings history"), crumb{Label: s.t("Machines"), Href: consolePath("/machines")}, crumb{Label: name, Href: path}),
		L: machineHistoryLabels{
			Version: s.t("Version"), Change: s.t("Change"), Who: s.t("Changed by"), Time: s.t("Time"),
			Account: s.t("Account"), Critical: s.t("Critical"), Note: s.t("Note"), Restore: s.t("Restore settings"),
			Older: s.t("Older versions"), Empty: s.t("Settings history starts with the first edit."),
			Scope:   s.t("Restoring copies the account, critical flag and note into a new version. Machine identity and keys stay as they are."),
			Current: s.t("Current"),
		},
		Current: history.Version, CanRestore: v.has("machines:write"), Post: path + "/restore",
	}
	for _, rev := range history.Revisions[:min(limit, len(history.Revisions))] {
		who := rev.Actor
		if rev.Via != "" {
			who = s.t("%s via %s", who, s.via(rev.Via))
		}
		c := s.t("No")
		if rev.Metadata.Critical {
			c = s.t("Yes")
		}
		p.Rows = append(p.Rows, machineRevisionRow{Version: rev.Version, Current: rev.Version == history.Version,
			Who: who, Time: s.clock(rev.At), Change: s.machineRevisionName(rev.Action, rev.Restored),
			User: rev.Metadata.User, Critical: c, Note: rev.Metadata.Note, Restore: s.t("Restore settings from version %d", rev.Version)})
	}
	if len(history.Revisions) > limit {
		p.Older = fmt.Sprintf("%s/history?before=%d", path, history.Revisions[limit-1].Version)
	}
	s.render(w, machineHistoryTmpl, p)
}

func (s *Server) machineRevisionName(action string, restored int64) string {
	switch action {
	case "registered":
		return s.t("Registered")
	case "baseline":
		return s.t("Before the first tracked edit")
	case "edited":
		return s.t("Edited")
	case "restored":
		return s.t("Restored settings from version %d", restored)
	}
	return action
}

func (s *Server) restoreMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	page := machinePath(name) + "/history"
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	current, versionErr := strconv.ParseInt(r.PostForm.Get("if_version"), 10, 64)
	if err != nil || versionErr != nil {
		back(w, r, page, "error", "machine.changed")
		return
	}
	out, err := s.SSH.RestoreMachine(r.Context(), v.gate(), name, sshgate.MachineRestore{Version: version, IfVersion: current})
	if err != nil {
		s.machineErr(w, r, page, err)
		return
	}
	done := "machine_restored"
	if out.Version == current {
		done = "machine_unchanged"
	}
	back(w, r, page, "done", done)
}

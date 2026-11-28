package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The console's forms. Each goes through the method the API route calls, as the signed-in token with
// Via "console", so that it is audited and echoed to the chat like everything else; then the page it
// came from is shown again, saying what happened.

// back sends the person to path, a page of the console, with done (what was done) or a problem's code.
func back(w http.ResponseWriter, _ *http.Request, path, param, code string) {
	to, _ := url.Parse(local(path))
	q := to.Query()
	q.Set(param, code)
	to.RawQuery = q.Encode()
	redirectLocal(w, to.String(), http.StatusSeeOther)
}

// problemCode names what went wrong, for the page to say in its own words.
func problemCode(err error) string {
	switch {
	case errors.Is(err, bridge.ErrNotFiring):
		return "not_firing"
	case errors.Is(err, bridge.ErrAckChanged):
		return "ack_changed"
	case errors.Is(err, bridge.ErrEpisodeChanged):
		return "incident_changed"
	case errors.Is(err, bridge.ErrBadInput):
		return "bad_input"
	case errors.Is(err, configdb.ErrConflict):
		return "conflict"
	case errors.Is(err, configdb.ErrInvalid):
		return "invalid"
	case errors.Is(err, sshgate.ErrDisabled):
		return "ssh_off"
	case errors.Is(err, bridge.ErrForbidden):
		return "forbidden"
	case errors.Is(err, store.ErrLastOwner):
		return "last_owner"
	}
	return "failed"
}

// problem is what a problem's code says.
func (s *Server) problem(r *http.Request, code string) string {
	switch code {
	case "":
		return ""
	case "not_firing":
		return s.t("The alert is not firing any more.")
	case "ack_changed":
		return s.t("The acknowledgement changed. Review who is responding and try again.")
	case "incident_changed":
		return s.t("This incident has changed. Review the alert before taking action.")
	case "bad_input":
		return s.t("That did not work: say why in the text, and keep it under 2,000 characters.")
	case "credential_denied":
		return s.t("You cannot make this change to the credential or grant.")
	case "credential_invalid":
		return s.t("Check the credential details, the approved HTTPS endpoint or the agent's scope.")
	case "credential_changed":
		return s.t("The credential has changed. Check its current details and try again.")
	case "credential_upstream":
		return s.t("The configured service could not be reached.")
	case "credential_unavailable":
		return s.t("The team credential store cannot be verified.")
	case "conflict":
		return s.t("A later change touched the same items; revert that one first, or change them through the API.")
	case "invalid":
		return s.t("The configuration would not pass its checks after this change, so nothing changed.")
	case "ssh_off", "ssh.disabled":
		return s.t("The SSH channel is off.")
	case "machine.exists":
		return s.t("A machine with this name exists already.")
	case "machine.unreachable":
		return s.t("The machine could not be reached, so nothing was added.")
	case "machine.not_found":
		return s.t("There is no such machine, or no longer.")
	case "machine.version_not_found":
		return s.t("That version does not belong to the machine's current registration.")
	case "machine.changed":
		return s.t("The machine changed. Review the current settings and try again.")
	case "confirm":
		return s.t("Type the machine's name exactly to remove it.")
	case "not_revocable":
		return s.t("Only tokens that reach machines, and agents' tokens, are revoked here; revoke the others with beacon token revoke on the host.")
	case "forbidden":
		return s.t("That is not yours to change: people manage their own agents, owners everyone's.")
	case "taken":
		return s.t("That name is taken; choose another.")
	case "not_found":
		return s.t("There is no such thing, or no longer.")
	case "probe_unavailable":
		return s.t("This Beacon has no gateway for on-demand checks.")
	case "probe_no_targets":
		return s.t("No eligible probe targets are configured for this alert.")
	case "probe_busy":
		return s.t("The probes are busy. Try again in a moment.")
	case "probe_not_recorded":
		return s.t("Beacon could not save the incident check. Try again.")
	case "window_over":
		return s.t("That maintenance window has ended already.")
	case "bad_agent":
		return s.t("That did not work: check the name (2 to 40 lower-case letters, digits, dots, dashes and underscores), the machines, and that the preset is within the owner's role.")
	case "machine_access":
		return s.t("Choose all machines that are not critical, or select specific machines.")
	case "machine_selection":
		return s.t("Select at least one machine.")
	case "machine_unknown":
		return s.t("A selected machine is no longer registered. Reload and select again.")
	case "key_taken":
		return s.t("That key is already registered. Use a separate key for each person, agent, token or machine.")
	case "bad_key":
		return s.t("That is not a public key in the authorized_keys form (ssh-ed25519 AAAA…).")
	case "last_owner":
		return s.t("Beacon would be left without an owner; make someone else owner first.")
	case "self":
		return s.t("You cannot disable your own account.")
	case "differ":
		return s.t("The passwords differ.")
	case "blocked":
		if s.Auth != nil {
			if d, ok := s.Auth.BlockedFor(r); ok {
				return s.lockedOutFromHere(d)
			}
		}
		return s.t("Too many failed sign-ins from here; try again in a few minutes.")
	case "password_short":
		return s.t("The password is shorter than 10 characters.")
	case "password_long":
		return s.t("The password is too long.")
	case "password_username":
		return s.t("The password may not be the username.")
	case "password_common":
		return s.t("That password is one of the commonest; choose another.")
	case "password_wrong":
		return s.t("The current password is wrong.")
	case "not_legacy":
		return s.t("Only a legacy token, with people's scopes and no agent yet, is claimed.")
	case "claim_wider":
		return s.t("That preset gives more than the token may do now; keep its scopes or choose less.")
	}
	return s.t("That did not work; the log says why.")
}

func (s *Server) failed(w http.ResponseWriter, r *http.Request, path, what string, err error) {
	code := problemCode(err)
	if code == "failed" {
		s.log().Error("console action failed", "action", what, "err", err)
	}
	back(w, r, path, "error", code)
}

func alertActionPage(r *http.Request) string {
	episode := r.PostForm.Get("return_episode")
	if episode != r.PostForm.Get("expected_episode") || !link.ValidEpisode(episode) {
		episode = ""
	}
	return link.IncidentPath(r.PathValue("id"), episode)
}

func (s *Server) ack(w http.ResponseWriter, r *http.Request, v *viewer) {
	id := r.PathValue("id")
	page := alertActionPage(r)
	if _, err := s.Bridge.AckIfUnchanged(r.Context(), id, v.actor(), r.PostForm.Get("note"), r.PostForm.Get("expected_ack")); err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			s.missing(w, r, v)
			return
		}
		s.failed(w, r, page, "ack", err)
		return
	}
	back(w, r, page, "done", "ack")
}

// silences are the lengths the console offers; the bridge takes up to 24 hours.
var silences = map[string]time.Duration{"1h": time.Hour, "4h": 4 * time.Hour, "24h": 24 * time.Hour}

func (s *Server) silence(w http.ResponseWriter, r *http.Request, v *viewer) {
	id := r.PathValue("id")
	page := alertActionPage(r)
	d, ok := silences[r.PostForm.Get("duration")]
	if !ok {
		back(w, r, page, "error", "bad_input")
		return
	}
	if _, err := s.Bridge.SilenceIncident(r.Context(), id, r.PostForm.Get("expected_episode"), d, v.actor(), r.PostForm.Get("comment")); err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			s.missing(w, r, v)
			return
		}
		s.failed(w, r, page, "silence", err)
		return
	}
	back(w, r, page, "done", "silence")
}

func (s *Server) note(w http.ResponseWriter, r *http.Request, v *viewer) {
	id := r.PathValue("id")
	page := alertActionPage(r)
	if _, err := s.Bridge.AddIncidentNote(r.Context(), id, r.PostForm.Get("expected_episode"), v.actor(), r.PostForm.Get("text")); err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			s.missing(w, r, v)
			return
		}
		s.failed(w, r, page, "note", err)
		return
	}
	back(w, r, page, "done", "note")
}

func (s *Server) probeNow(w http.ResponseWriter, r *http.Request, v *viewer) {
	id := r.PathValue("id")
	page := alertActionPage(r)
	episode := r.PostForm.Get("expected_episode")
	result, err := s.Bridge.ProbeNow(r.Context(), v.actor(), bridge.ProbeNowInput{AlertID: id, Episode: &episode})
	switch {
	case errors.Is(err, bridge.ErrNotFound):
		s.missing(w, r, v)
	case errors.Is(err, bridge.ErrProbeUnavailable):
		back(w, r, page, "error", "probe_unavailable")
	case errors.Is(err, bridge.ErrBadInput), errors.Is(err, bridge.ErrProbeNotFound):
		back(w, r, page, "error", "probe_no_targets")
	case errors.Is(err, bridge.ErrProbeBusy):
		back(w, r, page, "error", "probe_busy")
	case errors.Is(err, bridge.ErrProbeNotRecorded):
		back(w, r, page, "error", "probe_not_recorded")
	case err != nil:
		s.failed(w, r, page, "probe on demand", err)
	default:
		http.Redirect(w, r, probeResultPath(page, result.ID), http.StatusSeeOther)
	}
}

func (s *Server) pause(paused bool) func(http.ResponseWriter, *http.Request, *viewer) {
	return func(w http.ResponseWriter, r *http.Request, v *viewer) {
		if err := s.SSH.SetPaused(r.Context(), v.gate(), paused); err != nil {
			s.failed(w, r, consolePath("/machines"), "pause", err)
			return
		}
		done := "resumed"
		if paused {
			done = "paused"
		}
		back(w, r, consolePath("/machines"), "done", done)
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	err := s.Bridge.RevokeToken(r.Context(), v.actor(), name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.missing(w, r, v)
	case errors.Is(err, bridge.ErrBadInput):
		back(w, r, consolePath("/tokens"), "error", "not_revocable")
	case err != nil:
		s.failed(w, r, consolePath("/tokens"), "revoke", err)
	case v.Token != nil && name == v.Token.Name: // its own token: the sign-in ended with it
		http.SetCookie(w, s.cookie(sessionCookie, "", time.Time{}))
		http.Redirect(w, r, consolePath("/sign-in"), http.StatusSeeOther)
	default:
		back(w, r, consolePath("/tokens"), "done", "revoked")
	}
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request, v *viewer) {
	n, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || n < 1 || s.Config == nil {
		s.missing(w, r, v)
		return
	}
	page := consolePath("/config/history/") + strconv.FormatInt(n, 10)
	ch, err := s.Config.Revert(r.Context(), configdb.Actor{Name: v.name(), Via: "console", Principal: &v.Principal}, n, r.PostForm.Get("note"))
	switch {
	case errors.Is(err, configdb.ErrNotFound):
		s.missing(w, r, v)
	case err != nil:
		s.failed(w, r, page, "revert", err)
	default:
		back(w, r, consolePath("/config/history/")+strconv.FormatInt(ch.Version, 10), "done", "reverted")
	}
}

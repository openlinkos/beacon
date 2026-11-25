package web

import (
	"encoding/hex"
	"net/url"
	"strings"

	"github.com/openlinkos/beacon/internal/web/link"
)

const sshIncidentParam = "incident"

// sshIncidentPath accepts a group and episode reference, not a caller-supplied return URL.
func sshIncidentPath(ref string) string {
	id, episode, ok := strings.Cut(ref, ".")
	if !ok || len(id) != 12 || !link.ValidEpisode(episode) {
		return ""
	}
	if _, err := hex.DecodeString(id); err != nil {
		return ""
	}
	return link.IncidentPath(id, episode)
}

func incidentMachinePath(name, registration, ref string) string {
	return href(machinePath(name), url.Values{"registration": {registration}, sshIncidentParam: {ref}}) + "#ssh-connect"
}

type incidentKeyReturn struct {
	Ref, Machine, Registration string
}

// incidentKeyReturn keeps the incident when the machine or the person's access changes during setup.
func (s *Server) incidentKeyReturn(v *viewer, q url.Values) incidentKeyReturn {
	ref := q.Get(sshIncidentParam)
	if !v.person() || !v.has("machines:ssh") || sshIncidentPath(ref) == "" {
		return incidentKeyReturn{}
	}
	out := incidentKeyReturn{Ref: ref}
	if machine, ok := s.Store.Machine(q.Get("ssh_machine")); ok && machine.Registration() == q.Get("registration") && v.Principal.Reaches(machine.Name) {
		out.Machine, out.Registration = machine.Name, machine.Registration()
	}
	return out
}

func (p incidentKeyReturn) accountPath() string {
	base := consolePath("/account")
	if p.Ref == "" {
		return base
	}
	q := url.Values{sshIncidentParam: {p.Ref}}
	if p.Machine != "" {
		q.Set("ssh_machine", p.Machine)
		q.Set("registration", p.Registration)
	}
	return href(base, q) + "#ssh-keys"
}

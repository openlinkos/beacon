package web

import (
	"net/http"

	"github.com/openlinkos/beacon/internal/store"
)

// The backups page: the recovery material, once. When Beacon generates the backup key pair
// itself (no recipients configured), the private half waits in the data directory as recovery
// material; this page does not embed it — an owner signed in with an account downloads it
// (the first download consumes it) and — only on the owner's word that it is stored — removes
// it from the server, from which point only the public keys remain and the page has nothing
// to show. Deliberately not an /api/v1 operation and not an MCP tool: no agent and no token
// may ever read the recovery material; a person signed in as an owner may.

// BackupConsole is what the backups page needs of the backup manager.
type BackupConsole interface {
	// RecoveryMaterial is the one-time material, still waiting to be collected.
	RecoveryMaterial() (b []byte, ok bool)
	// ConsumeRecoveryMaterial returns it and deletes it; the first download is the last.
	ConsumeRecoveryMaterial() ([]byte, error)
	// RemoveRecoveryMaterial deletes it; only the public keys stay.
	RemoveRecoveryMaterial() error
	// PublicKey is the age public half the server keeps (empty when none was generated here).
	PublicKey() string
	// SignPublicKey is the manifest-signing public key a restore verifies with.
	SignPublicKey() string
}

type backupLabels struct {
	Off, OffHint, Waiting, WaitingHint, Warn, WarnHint, Download, Remove, RemoveHint,
	PublicKey, PublicKeyHint, SignPublic, SignPublicHint, Stored, StoredHint string
}

type backupPage struct {
	shell
	L           backupLabels
	Off         bool
	HasMaterial bool
	Download    string
	Remove      string
	PublicKey   string
	SignPublic  string
}

var backupTmpl = parse("backup.html")

func (s *Server) backupPerson(w http.ResponseWriter, r *http.Request, v *viewer) bool {
	if v.person() {
		return true
	}
	s.message(w, r, v, http.StatusForbidden, s.t("Not with this token"),
		s.t("The recovery material is only for a person signed in with an account."))
	return false
}

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	if !s.backupPerson(w, r, v) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p := backupPage{shell: s.shellFor(r, v, "backups", s.t("Backups")), L: backupLabels{
		Off:            s.t("Backups are off"),
		OffHint:        s.t("The configuration names no backup destination. Add one under backup.destinations and restart, and Beacon backs itself up from then on."),
		Waiting:        s.t("The recovery material is waiting"),
		WaitingHint:    s.t("No recipients were configured, so Beacon generated a key pair at its first start and encrypted every backup to its public half. The private half is the only thing that can read the backups. Download it once; this page does not show it."),
		Warn:           s.t("Whoever holds this file can read every backup"),
		WarnHint:       s.t("Store it somewhere safe that is not this machine (a password manager, an offline drive). Until it is removed here, Beacon keeps reminding you in its log."),
		Download:       s.t("Download the recovery material"),
		Remove:         s.t("I have stored it — remove it from the server"),
		RemoveHint:     s.t("After this the page cannot show it again: the server keeps only the public key. A backup and this file are all a recovery needs."),
		PublicKey:      s.t("The public key the server keeps"),
		PublicKeyHint:  s.t("Backups are encrypted to this; it is public, and this is the only encryption key the server holds."),
		SignPublic:     s.t("The signing public key"),
		SignPublicHint: s.t("A restore checks each archive against this; keep it with the recovery material."),
		Stored:         s.t("Only the public key remains"),
		StoredHint:     s.t("The recovery material is stored off the server, as it should be. A backup and that file are all a recovery needs; beacon backup drill proves the chain while you do not."),
	}}
	if s.Backups == nil {
		p.Off = true
		s.render(w, backupTmpl, p)
		return
	}
	_, ok := s.Backups.RecoveryMaterial()
	p.HasMaterial = ok
	p.Download = consolePath("/backup/recovery.txt")
	p.Remove = consolePath("/backup/remove")
	p.PublicKey = s.Backups.PublicKey()
	p.SignPublic = s.Backups.SignPublicKey()
	if ok {
		_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: v.name(), Via: "console",
			Action: "backup.recovery_viewed", Detail: "the recovery material page was viewed"})
	}
	s.render(w, backupTmpl, p)
}

// backupRecoveryFile serves the waiting material as a download, once, to an owner: the first
// successful download deletes it on the server.
func (s *Server) backupRecoveryFile(w http.ResponseWriter, r *http.Request, v *viewer) {
	if !s.backupPerson(w, r, v) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.Backups == nil {
		http.Redirect(w, r, consolePath("/backup"), http.StatusSeeOther)
		return
	}
	material, err := s.Backups.ConsumeRecoveryMaterial()
	if err != nil || len(material) == 0 {
		http.Redirect(w, r, consolePath("/backup"), http.StatusSeeOther)
		return
	}
	_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: v.name(), Via: "console",
		Action: "backup.recovery_downloaded", Detail: "the recovery material was downloaded and deleted on the server"})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="beacon-recovery-material.txt"`)
	_, _ = w.Write(material)
}

// backupRemove deletes the material on the owner's word that it is stored: only the public keys
// stay, and the reminders stop.
func (s *Server) backupRemove(w http.ResponseWriter, r *http.Request, v *viewer) {
	if !s.backupPerson(w, r, v) {
		return
	}
	if s.Backups == nil {
		http.Redirect(w, r, consolePath("/backup"), http.StatusSeeOther)
		return
	}
	if _, ok := s.Backups.RecoveryMaterial(); !ok {
		http.Redirect(w, r, consolePath("/backup"), http.StatusSeeOther)
		return
	}
	if err := s.Backups.RemoveRecoveryMaterial(); err != nil {
		s.message(w, r, v, http.StatusInternalServerError, s.t("The recovery material could not be removed"), err.Error())
		return
	}
	_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: v.name(), Via: "console",
		Action: "backup.recovery_removed", Detail: "the server keeps only the public key now"})
	http.Redirect(w, r, consolePath("/backup?done=recovery_removed"), http.StatusSeeOther)
}

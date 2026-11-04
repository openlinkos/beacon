package web

import (
	"net/http"

	"github.com/openlinkos/beacon/internal/store"
)

// The backups page: the recovery material, once. When Beacon generates the backup key pair
// itself (no recipients configured), the private half waits in the data directory as recovery
// material; this page shows it to an owner, serves it as a download, and — only on the owner's
// word that it is stored — removes it from the server, from which point only the public key
// remains and the page has nothing to show. Deliberately not an /api/v1 operation and not an
// MCP tool: no agent and no token may ever read the recovery material; a person signed in as an
// owner may.

// BackupConsole is what the backups page needs of the backup manager.
type BackupConsole interface {
	// RecoveryMaterial is the one-time material, still waiting to be collected.
	RecoveryMaterial() (b []byte, ok bool)
	// RemoveRecoveryMaterial deletes it; only the public key stays.
	RemoveRecoveryMaterial() error
	// PublicKey is the public half the server keeps (empty when none was generated here).
	PublicKey() string
}

type backupLabels struct {
	Off, OffHint, Waiting, WaitingHint, Warn, WarnHint, Download, Remove, RemoveHint,
	PublicKey, PublicKeyHint, Stored, StoredHint string
}

type backupPage struct {
	shell
	L           backupLabels
	Off         bool
	Material    string
	HasMaterial bool
	Download    string
	Remove      string
	PublicKey   string
}

var backupTmpl = parse("backup.html")

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := backupPage{shell: s.shellFor(r, v, "backups", s.t("Backups")), L: backupLabels{
		Off:           s.t("Backups are off"),
		OffHint:       s.t("The configuration names no backup destination. Add one under backup.destinations and restart, and Beacon backs itself up from then on."),
		Waiting:       s.t("The recovery material is waiting"),
		WaitingHint:   s.t("No recipients were configured, so Beacon generated a key pair at its first start and encrypted every backup to its public half. The private half — below — is the only thing that can read the backups. This page is the one place it is shown."),
		Warn:          s.t("Whoever holds this file can read every backup"),
		WarnHint:      s.t("Store it somewhere safe that is not this machine (a password manager, an offline drive). Until it is removed here, Beacon keeps reminding you in its log."),
		Download:      s.t("Download the recovery material"),
		Remove:        s.t("I have stored it — remove it from the server"),
		RemoveHint:    s.t("After this the page cannot show it again: the server keeps only the public key. A backup and this file are all a recovery needs."),
		PublicKey:     s.t("The public key the server keeps"),
		PublicKeyHint: s.t("Backups are encrypted to this; it is public, and this is the only key the server holds."),
		Stored:        s.t("Only the public key remains"),
		StoredHint:    s.t("The recovery material is stored off the server, as it should be. A backup and that file are all a recovery needs; beacon backup drill proves the chain while you do not."),
	}}
	if s.Backups == nil {
		p.Off = true
		s.render(w, backupTmpl, p)
		return
	}
	material, ok := s.Backups.RecoveryMaterial()
	p.HasMaterial, p.Material = ok, string(material)
	p.Download = consolePath("/backup/recovery.txt")
	p.Remove = consolePath("/backup/remove")
	p.PublicKey = s.Backups.PublicKey()
	s.render(w, backupTmpl, p)
}

// backupRecoveryFile serves the waiting material as a download, once, to an owner.
func (s *Server) backupRecoveryFile(w http.ResponseWriter, r *http.Request, v *viewer) {
	material, ok := s.Backups.RecoveryMaterial()
	if !ok {
		http.Redirect(w, r, consolePath("/backup"), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="beacon-recovery-material.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(material)
}

// backupRemove deletes the material on the owner's word that it is stored: only the public key
// stays, and the reminders stop.
func (s *Server) backupRemove(w http.ResponseWriter, r *http.Request, v *viewer) {
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

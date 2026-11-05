package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/backup"
)

func TestBackupKeygenPrintsThePrivateKeyOnce(t *testing.T) {
	var out, errb bytes.Buffer
	if code := backupCmd([]string{"keygen"}, &out, &errb); code != 0 {
		t.Fatalf("keygen: %d %s", code, errb.String())
	}
	priv := strings.TrimSpace(out.String())
	if !strings.HasPrefix(priv, "AGE-SECRET-KEY-") {
		t.Fatalf("stdout: %q", out.String())
	}
	all := out.String() + errb.String()
	if strings.Count(all, "AGE-SECRET-KEY-") != 1 {
		t.Fatalf("the private key printed more than once")
	}
	if strings.Contains(errb.String(), priv) {
		t.Fatal("stderr repeated the private key")
	}
}

func TestBackupRestoreWithoutConfirmIsRefused(t *testing.T) {
	d := t.TempDir()
	tok := filepath.Join(d, "tok")
	if err := os.WriteFile(tok, []byte("0123456789abcdef0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(d, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(d, "config.yaml")
	body := "data_dir: " + filepath.ToSlash(data) + "\nrules: {token_file: " + filepath.ToSlash(tok) + "}\nvictoriametrics: {url: http://vm:8428}\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := backup.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	_, sign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := filepath.Join(d, "id")
	material := k.Private + "\n" + backup.FormatSignPublic(sign.Public().(ed25519.PublicKey)) + "\n"
	if err := os.WriteFile(id, []byte(material), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := backupCmd([]string{"restore", filepath.Join(d, "missing"+backup.Suffix),
		"-config", cfg, "-identity", id}, &out, &errb)
	if code == 0 {
		t.Fatal("a restore into the live directory without -confirm succeeded")
	}
	if !strings.Contains(errb.String(), "-confirm") {
		t.Fatalf("the refusal: %s", errb.String())
	}
}

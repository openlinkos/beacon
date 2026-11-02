package configdb

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestModuleSecretFiles: a change through Beacon that points a module's password_file anywhere but a
// file name in the probe's secrets directory is refused, whether put or imported, and nothing changes.
func TestModuleSecretFiles(t *testing.T) {
	e := imported(t)
	before := e.s.Version()
	for name, value := range map[string]string{
		"absolute":   `{"prober": "git", "git": {"username": "u", "password_file": "/etc/passwd"}}`,
		"parent":     `{"prober": "registry", "registry": {"repository": "library/busybox", "password_file": "../token"}}`,
		"separator":  `{"prober": "git", "git": {"password_file": "secrets/git"}}`,
		"backslash":  `{"prober": "git", "git": {"password_file": "secrets\\git"}}`,
		"drive path": `{"prober": "git", "git": {"password_file": "C:\\secrets\\git"}}`,
	} {
		_, err := e.s.Put(ctx, api, "modules", "repo", json.RawMessage(value), "")
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "secrets directory") || !strings.Contains(err.Error(), "password_file") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.s.Version() != before || e.s.Modules() == nil || strings.Contains(string(e.s.Modules()), "repo") {
		t.Fatal("a refused change left something behind")
	}
	ch, err := e.s.Put(ctx, api, "modules", "repo", json.RawMessage(`{"prober": "git", "git": {"username": "u", "password_file": "git-password"}}`), "")
	if err != nil || ch.Version != before+1 {
		t.Fatalf("a file name in the secrets directory: %+v %v", ch, err)
	}

	f := testFiles()
	f[ModulesFile] = modulesYAML + "  repo:\n    prober: git\n    git: {username: u, password_file: /run/secrets/git}\n"
	if _, err := e.s.Import(ctx, api, f, "", false); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), ModulesFile) || !strings.Contains(err.Error(), "secrets directory") {
		t.Errorf("an import with a path: %v", err)
	}
	if _, err := ParseFiles(f); err == nil {
		t.Error("ParseFiles accepted the path")
	}
	if e.s.Version() != before+1 {
		t.Error("the refused import changed something")
	}
}

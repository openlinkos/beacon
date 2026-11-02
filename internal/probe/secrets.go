package probe

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// A module's file fields (password_file) name files on the probe's machine, and whoever holds
// config:write chooses their values. So that such a holder cannot have a probe read any file on its
// machine and send it as a credential, the values are file names in one directory, the secrets
// directory (linkos-probe -secrets-dir), never paths: the probe refuses anything else before reading
// (reason config), and the centre refuses it when a module is put or imported (config.invalid).

// DefaultSecretsDir is where a probe looks for the files its modules name.
const DefaultSecretsDir = "/etc/linkos-agent/secrets" //nolint:gosec // G101: a directory, not a credential

// SecretName checks the value of a module's file field: a file name to be found in the secrets
// directory, without a path. field names the field, for the error; an empty name is no file.
func SecretName(field, name string) error {
	switch {
	case name == "":
		return nil
	case strings.ContainsAny(name, `/\`), strings.Contains(name, ".."), filepath.IsAbs(name), name == ".",
		strings.ContainsFunc(name, unicode.IsControl):
		return fmt.Errorf("%s %q: put the file under the probe's secrets directory (%s by default; linkos-probe -secrets-dir) "+
			"and name it there without a path, such as %q", field, name, DefaultSecretsDir, "registry-password")
	}
	return nil
}

type secretFile struct{ field, name string }

// secretFiles lists the module's file fields: every *_file a module may carry. A new one goes here, and
// nowhere else, to be checked and read through the secrets directory.
func (m *Module) secretFiles() []secretFile {
	return []secretFile{{"registry.password_file", m.Registry.PasswordFile}, {"git.password_file", m.Git.PasswordFile}}
}

// CheckSecretFiles checks every module's file fields with SecretName. The centre refuses a module file
// that fails it. A probe still parses such a file (an older gateway may hand one out) and refuses each
// probe that would read the file, so that the other modules keep working.
func CheckSecretFiles(ms *Modules) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(ms.Modules)) {
		m := ms.Modules[name]
		if m == nil {
			continue
		}
		for _, f := range m.secretFiles() {
			if err := SecretName(f.field, f.name); err != nil {
				errs = append(errs, fmt.Errorf("module %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// secret reads the file a module's field names from the secrets directory, trimmed; an empty name is
// no secret. The probe never reads outside that directory for a module.
func (p *Prober) secret(field, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if err := SecretName(field, name); err != nil {
		return "", err
	}
	dir := p.SecretsDir
	if dir == "" {
		dir = DefaultSecretsDir
	}
	path := filepath.Join(dir, name)
	if filepath.Dir(path) != filepath.Clean(dir) {
		return "", fmt.Errorf("%s %q: not a file name in the secrets directory", field, name)
	}
	b, err := os.ReadFile(path) //nolint:gosec // a plain file name in the secrets directory, checked above
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	return strings.TrimSpace(string(b)), nil
}

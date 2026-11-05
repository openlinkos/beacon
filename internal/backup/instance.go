package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// instanceNameFile is the persisted instance name under <data>/backup, so a container
// recreate keeps the same host in archive names.
const instanceNameFile = "instance-name"

// instanceNameRe is the hostname-safe form ArchiveName already accepts, including dashes.
var instanceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ResolveInstanceName is the host that goes into archive names. A configured name is used
// as-is (and must match the hostname-safe form). Otherwise the first valid name is stored
// under <dataDir>/backup/instance-name so it survives a container recreate: the file if
// it is already valid, else the kernel hostname, else "beacon".
func ResolveInstanceName(dataDir, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if !instanceNameOK(configured) {
			return "", fmt.Errorf("backup.host is a hostname ([A-Za-z0-9][A-Za-z0-9._-]*)")
		}
		return configured, nil
	}
	path := filepath.Join(dataDir, "backup", instanceNameFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the name Beacon itself wrote
		name := strings.TrimSpace(string(b))
		if instanceNameOK(name) {
			return name, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	name, err := os.Hostname()
	name = strings.TrimSpace(name)
	if err != nil || !instanceNameOK(name) {
		name = "beacon"
	}
	if err := writeFileAtomic(path, []byte(name+"\n")); err != nil {
		return "", err
	}
	return name, nil
}

func instanceNameOK(s string) bool { return instanceNameRe.MatchString(s) }

func instanceNameForDir(dir string) (string, error) {
	dataDir := dir
	if filepath.Base(dir) == "backup" {
		dataDir = filepath.Dir(dir)
	}
	return ResolveInstanceName(dataDir, "")
}

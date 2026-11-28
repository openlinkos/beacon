package sshgate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupTrustsTheCAForOneAccount: the machine setup Beacon prints trusts its certificate authority for
// the machine's account alone, never for every account (root included).
func TestSetupTrustsTheCAForOneAccount(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	conf := filepath.Join(t.TempDir(), "beacon.conf")
	run := func(script string) string {
		t.Helper()
		script = strings.ReplaceAll(script, "/etc/ssh/sshd_config.d/beacon.conf", conf)
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", script, err, out)
		}
		b, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := run(TrustOneAccount("beacon", ">")); got != "Match User beacon\n  TrustedUserCAKeys /etc/ssh/beacon_ca.pub\nMatch all\n" {
		t.Errorf("the drop-in: %q", got)
	}

	e := setup(t)
	e.g.d.PublicAddress = "ssh.example.com:17002"
	a := e.addConnector(t, "nat", signer(t), "")
	var lines []string
	for _, l := range strings.Split(a.Connector.Script, "\n") {
		if strings.Contains(l, "sshd_config.d/beacon.conf") {
			lines = append(lines, l)
		}
	}
	got := run(strings.Join(lines, "\n"))
	if got != "PerSourcePenaltyExemptList 127.0.0.1,::1\nMatch User beacon\n  TrustedUserCAKeys /etc/ssh/beacon_ca.pub\nMatch all\n" {
		t.Errorf("the connector machine's drop-in: %q", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "TrustedUserCAKeys") {
			t.Errorf("the CA is trusted for every account: %q", got)
		}
	}
}

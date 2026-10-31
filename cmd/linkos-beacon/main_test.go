package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckConfig(t *testing.T) {
	d := t.TempDir()
	tok := filepath.Join(d, "tok")
	if err := os.WriteFile(tok, []byte("0123456789abcdef0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	cat := filepath.Join(d, "catalog")
	_ = os.Mkdir(cat, 0o700)
	_ = os.WriteFile(filepath.Join(cat, "locations.yaml"), []byte("locations: [{id: us-los-angeles, name: United States - Los Angeles, region: overseas}]\n"), 0o600)
	hook := filepath.Join(d, "hook")
	_ = os.WriteFile(hook, []byte("https://open.feishu.cn/open-apis/bot/v2/hook/x"), 0o600)
	cfg := filepath.Join(d, "config.yaml")
	body := "catalog_dir: " + filepath.ToSlash(cat) + "\nrules: {token_file: " + filepath.ToSlash(tok) + "}\nvictoriametrics: {url: http://vm:8428}\n" +
		"feishu: {webhook_url_file: " + filepath.ToSlash(hook) + "}\n" +
		"escalation: {critical: {channel: webhook}, warning: {channel: webhook}, info: {channel: webhook}}\n"
	_ = os.WriteFile(cfg, []byte(body), 0o600)

	var out, errb bytes.Buffer
	if code := run([]string{"check-config", "-config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("check-config: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "1 probe locations") {
		t.Errorf("output: %s", out.String())
	}
	_ = os.WriteFile(filepath.Join(cat, "bad.yaml"), []byte("locations: [{id: BAD}]\n"), 0o600)
	errb.Reset()
	if code := run([]string{"check-config", "-config", cfg}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "BAD") {
		t.Errorf("a bad catalogue: %d %s", code, errb.String())
	}
}

func TestUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if run(nil, &out, &errb) != 2 || run([]string{"bogus"}, &out, &errb) != 2 {
		t.Error("usage errors exit 2")
	}
	if run([]string{"version"}, &out, &errb) != 0 || !strings.Contains(out.String(), "linkos-beacon") {
		t.Error("version")
	}
	if run([]string{"drill", "sideways"}, &out, &errb) != 2 || run([]string{"token", "create"}, &out, &errb) != 2 {
		t.Error("bad subcommands exit 2")
	}
}

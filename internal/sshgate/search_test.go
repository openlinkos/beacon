package sshgate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

func TestSearchFilesReturnsExactPathsAndRecordedEvidence(t *testing.T) {
	needShell(t)
	e := setup(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const needle = "--literal [a].' $(touch search-should-not-run)"
	weird := write("nested/\u90e8\u7f72: quote\"\nnext.go", "ignored\n\x1b[31m"+needle+" \u8fd0\u884c\x1b[0m\n")
	write("nested/other.go", strings.ToUpper(needle)+"\n")
	write(".hidden", needle+"\n")
	write("binary", "before\x00"+needle+"\n")
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "plain.txt")
	if err := os.WriteFile(outsideFile, []byte(needle+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][2]string{{outside, "outside-directory"}, {outsideFile, "outside-file"}} {
		if err := os.Symlink(names[0], filepath.Join(root, names[1])); err != nil {
			t.Fatal(err)
		}
	}

	who := Actor{Name: "claude-code", Key: "token:claude-code", Via: "mcp"}
	got, err := e.g.SearchFiles(t.Context(), who, "box", SearchInput{Directory: root, Query: needle})
	if err != nil || got.Limited || got.TimedOut || got.ScannedFiles == nil || *got.ScannedFiles != 3 || len(got.Matches) != 1 {
		t.Fatalf("literal search: %+v, %v", got, err)
	}
	if m := got.Matches[0]; m.Path != filepath.ToSlash(weird) || m.Line != 2 || m.Text != needle+" \u8fd0\u884c" || m.Truncated {
		t.Fatalf("match did not preserve the exact path and clean source context: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(e.tg.home, "search-should-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("search data was interpreted as shell code")
	}
	if got.Session == "" || got.RecordingState != store.EvidenceComplete || got.TranscriptState != store.EvidenceComplete {
		t.Fatalf("search evidence is incomplete: %+v", got)
	}
	tr, err := e.g.Session(got.Session)
	if err != nil || tr.Machine != "box" || tr.Kind != "file" || tr.Principal != who.Key || tr.Via != "mcp" ||
		!strings.Contains(tr.Stdout, "\"type\":\"match\"") {
		t.Fatalf("search was not recorded as the acting principal: %+v, %v", tr, err)
	}
	if audit := e.st.AuditLog(1); len(audit) != 1 || audit[0].Action != "ssh_file" || audit[0].Target != "box" ||
		!strings.HasPrefix(audit[0].Detail, "search ") || strings.Contains(audit[0].Detail, needle) {
		t.Fatalf("search audit: %+v", audit)
	}
}

func TestSearchFilesReportsBoundsAndRefusesUnsafeRoots(t *testing.T) {
	needShell(t)
	e := setup(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "link")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}

	who := Actor{Name: "claude-code", Key: "token:claude-code", Via: "api"}
	for _, directory := range []string{"", "/", "relative", root + "/../" + filepath.Base(root), linked} {
		if _, err := e.g.SearchFiles(t.Context(), who, "box", SearchInput{Directory: directory, Query: "needle"}); !errors.Is(err, ErrBadInput) {
			t.Errorf("root %q gave %v", directory, err)
		}
	}

	data := "needle" + strings.Repeat("x", searchMaxPreview+20) + "\n" +
		strings.Repeat("a", searchMaxBytes) + "\nneedle beyond budget\n"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.g.SearchFiles(t.Context(), who, "box", SearchInput{Directory: root, Query: "needle", Limit: 1})
	if err != nil || !got.Limited || got.TimedOut || len(got.Matches) != 1 || !got.Matches[0].Truncated {
		t.Fatalf("clipped search claims completeness: %+v, %v", got, err)
	}
	got, err = e.g.SearchFiles(t.Context(), who, "box", SearchInput{Directory: root, Query: "beyond budget"})
	if err != nil || !got.Limited || len(got.Matches) != 0 {
		t.Fatalf("matching outside the byte budget: %+v, %v", got, err)
	}
	boundary := strings.Repeat("a", searchMaxBytes) + "Z"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(boundary), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = e.g.SearchFiles(t.Context(), who, "box", SearchInput{Directory: root, Query: "Z"})
	if err != nil || !got.Limited || len(got.Matches) != 0 {
		t.Fatalf("search matched the byte immediately after the cap: %+v, %v", got, err)
	}
}

func TestSearchFramesPreservePartialResultsWithoutInventingProgress(t *testing.T) {
	got := SearchResult{Directory: "/project", TimedOut: true, Limited: true}
	got.collect("{\"type\":\"match\",\"path\":\"first.txt\",\"line\":9,\"text\":\"match\"}\n{\"type\":\"match", 5)
	if got.ScannedFiles != nil || len(got.Matches) != 1 || got.Matches[0].Path != "/project/first.txt" || !got.Limited {
		t.Fatalf("partial reply: %+v", got)
	}
	var outsider SearchResult
	outsider.collect("{\"type\":\"match\",\"path\":\"../outside\",\"line\":1,\"text\":\"clipped\"}\n{\"type\":\"summary\",\"scanned_files\":1,\"limited\":false}\n", 5)
	if len(outsider.Matches) != 0 || !outsider.Limited {
		t.Fatalf("an escaping path survived structured validation: %+v", outsider)
	}
}

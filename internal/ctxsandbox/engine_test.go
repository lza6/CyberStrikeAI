package ctxsandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// helper: build an engine backed by a fresh MemoryIndex.
func newTestEngine(t *testing.T) (*Engine, *MemoryIndex) {
	t.Helper()
	idx := NewMemoryIndex()
	return &Engine{Index: idx}, idx
}

// hasSh reports whether a POSIX sh is callable in this environment. On
// Windows with Git Bash on PATH this is true, so the shell-based downgrade
// tests run for real rather than being blanket-skipped.
func hasSh() bool {
	_, err := exec.LookPath("sh")
	return err == nil
}

func skipIfNoSh(t *testing.T) {
	t.Helper()
	if !hasSh() {
		t.Skip("POSIX sh not on PATH; shell-based downgrade tests need sh (Git Bash on Windows)")
	}
}

func TestRun_RejectsEmptyCommand(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.Run(context.Background(), nil, ""); err == nil {
		t.Fatal("empty command should error")
	}
	if _, err := e.Run(context.Background(), []string{""}, ""); err == nil {
		t.Fatal("blank command should error")
	}
}

func TestRun_RejectsNilIndex(t *testing.T) {
	e := &Engine{Index: nil}
	if _, err := e.Run(context.Background(), []string{"echo", "hi"}, ""); err == nil {
		t.Fatal("nil index should error")
	}
}

func TestRun_SmallOutputReturnedVerbatim(t *testing.T) {
	skipIfNoSh(t)
	e, idx := newTestEngine(t)
	res, err := e.Run(context.Background(), []string{"echo", "hello-ctx"}, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Indexed {
		t.Fatalf("small output should not be indexed, got %+v", res)
	}
	if !strings.Contains(res.Text, "hello-ctx") {
		t.Fatalf("verbatim output missing payload: %q", res.Text)
	}
	if idx.Size() != 0 {
		t.Fatalf("index should be empty for small output, size=%d", idx.Size())
	}
}

func TestRun_LargeOutputForceIndexed(t *testing.T) {
	skipIfNoSh(t)
	e, idx := newTestEngine(t)
	// Generate ~150KB of stdout via seq/yes-style via printf loop in sh.
	// Each line ~100 bytes → need ~1500 lines.
	res, err := e.Run(context.Background(), []string{"sh", "-c", "for i in $(seq 1 2000); do printf 'line %d: portscan result banner service version %d\\n' $i $i; done"}, "")
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if !res.Indexed {
		t.Fatalf("large output must be indexed, got BytesIn=%d", res.BytesIn)
	}
	if res.Path == "" {
		t.Fatalf("indexed result must carry a spill label (Path), got empty")
	}
	if !strings.Contains(res.Text, "ctx_search") {
		t.Fatalf("pointer text should mention ctx_search: %q", res.Text)
	}
	if !strings.Contains(res.Text, res.Path) {
		t.Fatalf("pointer text should contain label %q: %q", res.Path, res.Text)
	}
	// BytesOut must be drastically smaller than BytesIn (the 98% reduction claim).
	if res.BytesOut >= res.BytesIn {
		t.Fatalf("reduction ineffective: in=%d out=%d", res.BytesIn, res.BytesOut)
	}
	if idx.Size() == 0 {
		t.Fatalf("index should contain chunks after large output, size=0")
	}
}

func TestRun_IntentVerdictReturnsOnlyTitles(t *testing.T) {
	skipIfNoSh(t)
	e, _ := newTestEngine(t)
	// ~8KB output with a distinctive section the intent should lock onto.
	script := "for i in $(seq 1 100); do echo \"section $i: nmap open port 22 ssh\"; echo \"  details line $i\"; done"
	res, err := e.Run(context.Background(), []string{"sh", "-c", script}, "port 22 ssh")
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if !res.Indexed {
		t.Fatalf("intent path above threshold must be indexed, got BytesIn=%d", res.BytesIn)
	}
	if strings.Contains(res.Text, "details line") {
		t.Fatalf("verdict leaked content lines: %q", res.Text)
	}
	if !strings.Contains(res.Text, "port 22 ssh") {
		t.Fatalf("verdict should surface matching title: %q", res.Text)
	}
	if !strings.Contains(res.Text, "ctx_search") {
		t.Fatalf("verdict should point to ctx_search: %q", res.Text)
	}
}

func TestRun_IntentBelowThresholdReturnsVerbatim(t *testing.T) {
	skipIfNoSh(t)
	e, _ := newTestEngine(t)
	// ~500 bytes, below IntentSearchThreshold even with intent.
	res, err := e.Run(context.Background(), []string{"echo", "tiny result"}, "anything")
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if res.Indexed {
		t.Fatalf("below-threshold + intent should not index, got %+v", res)
	}
	if !strings.Contains(res.Text, "tiny result") {
		t.Fatalf("verbatim payload missing: %q", res.Text)
	}
}

func TestRun_ExitErrorCapturesStderr(t *testing.T) {
	skipIfNoSh(t)
	e, _ := newTestEngine(t)
	res, _ := e.Run(context.Background(), []string{"sh", "-c", "echo to-stdout; echo to-stderr 1>&2; exit 3"}, "")
	if !strings.Contains(res.Text, "to-stdout") {
		t.Fatalf("stdout missing: %q", res.Text)
	}
	if !strings.Contains(res.Text, "to-stderr") {
		t.Fatalf("stderr should be captured: %q", res.Text)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestScrubEnv_RemovesDangerousVars(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"BASH_ENV=/etc/evil.sh",
		"LD_PRELOAD=/tmp/hook.so",
		"NODE_OPTIONS=--require /tmp/pwn.js",
		"HOME=/root",
		"GIT_SSH=/tmp/ssh-hook",
	}
	out := scrubEnv(in)
	for _, kv := range out {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		switch key {
		case "BASH_ENV", "LD_PRELOAD", "NODE_OPTIONS", "GIT_SSH":
			t.Fatalf("dangerous var %q survived scrub", key)
		}
	}
	found := false
	for _, kv := range out {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Fatal("safe var PATH should survive scrub")
	}
}

func TestFirstToken_StripsPathAndExe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nmap", "nmap"},
		{"/usr/bin/nmap", "nmap"},
		{"./local/scan", "scan"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		if got := firstToken(tc.in); got != tc.want {
			t.Fatalf("firstToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		if got := firstToken("C:\\tools\\nmap.exe"); got != "nmap" {
			t.Fatalf("windows exe strip failed: %q", got)
		}
	}
}

func TestCapRunes_NoMidGlyphCut(t *testing.T) {
	s := "你好世界hello"
	capped := capRunes(s, 4)
	runes := []rune(capped)
	// Last rune must be the ellipsis we appended, not a partial 你.
	if runes[len(runes)-1] != '…' {
		t.Fatalf("expected ellipsis suffix, got %q", capped)
	}
}

func TestMemoryIndex_SearchScopesBySource(t *testing.T) {
	m := NewMemoryIndex()
	m.IndexPlaintext("alpha section about portscan", "execute:nmap")
	m.IndexPlaintext("beta section about webshell", "execute:curl")
	hits := m.Search("portscan", "execute:nmap", 5)
	if len(hits) == 0 {
		t.Fatal("expected hits scoped to nmap source")
	}
	for _, h := range hits {
		if h.Doc.Source != "execute:nmap" {
			t.Fatalf("scope leak: got source %q", h.Doc.Source)
		}
	}
	// Cross-source search should find both when query matches alpha.
	hitsAll := m.Search("section", "", 10)
	if len(hitsAll) < 2 {
		t.Fatalf("unscoped search expected ≥2 hits, got %d", len(hitsAll))
	}
}

func TestMemoryIndex_IdempotentReindex(t *testing.T) {
	m := NewMemoryIndex()
	n1, _ := m.IndexPlaintext("duplicate content\nsame line", "execute:sh")
	n2, _ := m.IndexPlaintext("duplicate content\nsame line", "execute:sh")
	if n1 == 0 || n2 == 0 {
		t.Fatalf("expected chunks indexed both times: n1=%d n2=%d", n1, n2)
	}
	// Appending duplicates is intentional (BM25 ranks them together).
	if m.Size() != n1+n2 {
		t.Fatalf("size = %d, want %d (append-only)", m.Size(), n1+n2)
	}
}

func TestEngine_WorkdirApplied(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	// Create a marker file so pwd shows our dir.
	marker := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(marker, []byte("from-sandbox"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := NewMemoryIndex()
	e := &Engine{Index: idx, Workdir: dir}
	res, err := e.Run(context.Background(), []string{"cat", "marker.txt"}, "")
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if !strings.Contains(res.Text, "from-sandbox") {
		t.Fatalf("workdir not applied: %q", res.Text)
	}
}

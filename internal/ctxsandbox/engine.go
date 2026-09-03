// Package ctxsandbox provides the "think in code" execution surface for the
// context-mode token-efficiency subsystem. It runs a command in a sandboxed
// working directory, captures bounded stdout, and — when output exceeds the
// intent threshold — indexes it into an in-memory BM25 store and returns only
// a verdict (matching section titles + first-line previews). This mirrors
// context-mode's ctx_execute tool (server.ts:1647-2036) and its three-tier
// downgrade: small output returned verbatim, intent output reduced to a
// verdict, large output auto-indexed with only a pointer returned.
//
// The sandbox layer is deliberately decoupled from MCP/executor so it can be
// unit-tested in any environment (no CGO, no live DB). The MCP tool binding
// lives in internal/mcp/ctx_execute_tool.go and wires this engine into the
// agent-facing surface.
package ctxsandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"cyberstrike-ai/internal/ctxindex"
)

// Thresholds mirror context-mode server.ts:1979-1980.
const (
	IntentSearchThreshold = 5_000           // bytes; outputs above this w/ intent become a verdict
	LargeOutputThreshold  = 102_400         // bytes; outputs above this are force-indexed, pointer returned
	PreviewMaxRunes       = 120             // first-line preview cap in verdict
	HardCapBytes          = 100 * 1_000_000 // per-process stdout hard cap (executor.ts:251)
	DefaultTimeout        = 60 * time.Second
	MaxTimeout            = 10 * time.Minute
)

// Index is the minimal store contract the sandbox needs to spill + retrieve.
// It is satisfied by an in-memory implementation (see store.go) and, in CGO
// environments, by a SQLite+FTS5-backed store. Keeping it an interface lets
// the pure-logic layer stay CGO-free and testable.
type Index interface {
	// IndexPlaintext chunks content and indexes it under source label.
	// Returns the number of chunks indexed and a stable label the caller
	// hands back to the model as the "spill reference".
	IndexPlaintext(content, source string) (indexed int, label string)
	// Search runs a BM25 query scoped to source (empty = all sources),
	// returning at most maxResults ranked hits.
	Search(query, source string, maxResults int) []ctxindex.Scored
}

// Engine executes sandboxed commands and applies the three-tier downgrade.
type Engine struct {
	Index     Index
	SpillRoot string // workspace root; empty = os.MkdirTemp
	Workdir   string // fixed sandbox dir; empty = per-call temp
	// DisableEnvScrub opts out of the default dangerous-env scrubbing. The
	// zero value (false) means scrubbing is ENABLED, matching context-mode's
	// executor.ts default. Callers must opt out explicitly to inherit the
	// parent environment verbatim (rare; only for trusted local tooling).
	DisableEnvScrub bool
}

// Result is what the MCP layer renders into the ToolResult content.
type Result struct {
	// Text is the final string shown to the model: either raw stdout (small),
	// a verdict (intent path), or an indexed pointer (large path).
	Text string
	// Indexed indicates whether output was spilled to the Index.
	Indexed bool
	// BytesIn is the raw stdout byte length before any reduction.
	BytesIn int
	// BytesOut is the byte length of Text (post-reduction).
	BytesOut int
	// ExitCode of the sandboxed process (-1 if killed/timed out).
	ExitCode int
	// Path is the spill workspace path when Indexed is true.
	Path string
}

// Run executes cmd[0] with cmd[1:] in a sandboxed directory, applies the
// three-tier downgrade, and returns the bounded model-facing text.
//
// The function is pure with respect to DB/CGO: it only needs os/exec and the
// injected Index. intent="" disables the verdict path (level 1) but leaves the
// large-output force-index path (level 2) active.
func (e *Engine) Run(ctx context.Context, cmd []string, intent string) (*Result, error) {
	if e == nil {
		return nil, errors.New("ctxsandbox: nil engine")
	}
	if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
		return nil, errors.New("ctxsandbox: empty command")
	}
	if e.Index == nil {
		return nil, errors.New("ctxsandbox: nil index store")
	}

	timeout := DefaultTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 {
			timeout = remaining
		}
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	execCmd := exec.CommandContext(cctx, cmd[0], cmd[1:]...)
	if e.Workdir != "" {
		execCmd.Dir = e.Workdir
	}
	if !e.DisableEnvScrub {
		execCmd.Env = scrubEnv(execCmd.Env)
	}

	stdout, code, err := e.collectBounded(cctx, execCmd)
	result := &Result{BytesIn: len(stdout), ExitCode: code}
	if err != nil && stdout == "" {
		result.Text = fmt.Sprintf("ctx_execute error: %v", err)
		result.BytesOut = len(result.Text)
		return result, nil
	}

	// Level 2: large output → force-index, return pointer only.
	if len(stdout) > LargeOutputThreshold {
		source := "execute:" + firstToken(cmd[0])
		n, label := e.Index.IndexPlaintext(stdout, source)
		result.Indexed = true
		result.Path = label
		result.Text = fmt.Sprintf(
			"Indexed %d sections from: %s\nUse ctx_search(queries: [\"...\"]) to query this content. Use source: %q to scope results.",
			n, label, label,
		)
		result.BytesOut = len(result.Text)
		return result, nil
	}

	// Level 1: intent + above threshold → verdict.
	intent = strings.TrimSpace(intent)
	if intent != "" && len(stdout) > IntentSearchThreshold {
		source := "execute:" + firstToken(cmd[0])
		// Always index first so the BM25 search has content to rank (cold path
		// where the index doesn't yet hold this run's output).
		n, label := e.Index.IndexPlaintext(stdout, source)
		result.Indexed = true
		result.Path = label
		hits := e.Index.Search(intent, label, 8)
		verdict := ctxindex.BuildVerdict(hits, intent, 8)
		_ = n
		result.Text = verdict
		result.BytesOut = len(result.Text)
		return result, nil
	}

	// Level 0: small output → return verbatim (bounded to a safety cap).
	result.Text = capRunes(stdout, 50_000)
	result.BytesOut = len(result.Text)
	return result, nil
}

// collectBounded runs the command and streams stdout into a bounded buffer,
// killing the process the moment output exceeds HardCapBytes. This prevents
// runaway producers (yes, cat /dev/urandom, an unbounded nmap -oN -) from
// exhausting memory before the cap bites. Mirrors context-mode executor.ts
// :514-537 (streaming hard cap).
//
// stderr is collected separately (bounded) so non-zero exits still surface
// their error text to the model.
func (e *Engine) collectBounded(ctx context.Context, cmd *exec.Cmd) (string, int, error) {
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", -1, fmt.Errorf("stdout pipe: %w", err)
	}
	// Capture stderr bounded as well so ExitError text survives.
	var stderrBuf strings.Builder
	cmd.Stderr = newBoundedErrWriter(&stderrBuf, 64*1024)
	if err := cmd.Start(); err != nil {
		return "", -1, fmt.Errorf("start: %w", err)
	}

	// Stream stdout up to HardCapBytes+1; the +1 lets us detect overflow and
	// kill the producer rather than silently truncating.
	buf := make([]byte, 0, 64*1024)
	overflow := false
	copied, copyErr := io.CopyN(newAppendWriter(&buf), pipe, HardCapBytes+1)
	if copyErr != nil && copyErr != io.EOF {
		// Non-EOF read error: record but keep what we have.
		_ = copyErr
	}
	if copied >= HardCapBytes+1 {
		overflow = true
	}
	_ = pipe.Close()

	// Kill if still running (overflow or context cancel). Ignore kill errors.
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	// If killed mid-stream, treat as a non-fatal overflow rather than an error
	// return — we still have bounded output to reduce/index.
	out := buf
	if overflow && len(out) > HardCapBytes {
		out = out[:HardCapBytes]
	}
	if stderrBuf.Len() > 0 {
		out = append(out, []byte("\n[stderr]\n")...)
		out = append(out, stderrBuf.String()...)
		if len(out) > HardCapBytes {
			out = out[:HardCapBytes]
		}
	}
	_ = waitErr
	return string(out), code, nil
}

// appendWriter is an io.Writer backed by a *[]byte for streaming collection.
type appendWriter struct{ b *[]byte }

func newAppendWriter(b *[]byte) *appendWriter { return &appendWriter{b: b} }
func (w *appendWriter) Write(p []byte) (int, error) {
	*w.b = append(*w.b, p...)
	return len(p), nil
}

// boundedErrWriter caps total bytes written; further writes are silently
// dropped. Prevents a chatty stderr from dominating the bounded output.
type boundedErrWriter struct {
	b    *strings.Builder
	max  int
	used int
}

func newBoundedErrWriter(b *strings.Builder, max int) *boundedErrWriter {
	return &boundedErrWriter{b: b, max: max}
}
func (w *boundedErrWriter) Write(p []byte) (int, error) {
	if w.used >= w.max {
		return len(p), nil
	}
	room := w.max - w.used
	if len(p) > room {
		p = p[:room]
	}
	n, _ := w.b.Write(p)
	w.used += n
	return len(p), nil
}

// scrubEnv removes environment variables known to enable code injection or
// library hijacking when running untrusted scripts. Ported from
// context-mode executor.ts:579-672.
func scrubEnv(in []string) []string {
	dangerous := map[string]struct{}{
		"BASH_ENV":              {},
		"ENV":                   {},
		"ZDOTDIR":               {},
		"NODE_OPTIONS":          {},
		"NODE_PATH":             {},
		"PYTHONSTARTUP":         {},
		"PYTHONPATH":            {},
		"PYTHONHOME":            {},
		"PERL5OPT":              {},
		"PERL5LIB":              {},
		"PERLLIB":               {},
		"RUBYOPT":               {},
		"RUBYLIB":               {},
		"LD_PRELOAD":            {},
		"LD_LIBRARY_PATH":       {},
		"DYLD_INSERT_LIBRARIES": {},
		"DYLD_LIBRARY_PATH":     {},
		"GIT_SSH":               {},
		"GIT_SSH_COMMAND":       {},
		"GIT_ASKPASS":           {},
		"SSH_AUTH_SOCK":         {},
		"SSH_AGENT_PID":         {},
		"IFS":                   {},
		"PS1":                   {},
		"PS2":                   {},
		"PS3":                   {},
		"PS4":                   {},
	}
	out := make([]string, 0, len(in))
	for _, kv := range in {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, bad := dangerous[key]; bad {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// firstToken returns the program name without a path, for use as a source
// label suffix (e.g. "execute:nmap").
func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndexByte(s, '\\'); i >= 0 {
		s = s[i+1:]
	}
	if runtime.GOOS == "windows" {
		s = strings.TrimSuffix(s, ".exe")
	}
	return s
}

// capRunes trims s to at most maxRunes runes without cutting a multi-byte
// glyph, appending an ellipsis when truncated. Mirrors truncate.ts capBytes
// semantics at the rune level.
func capRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	n := utf8.RuneCountInString(s)
	if n <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "…"
}

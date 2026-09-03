// Package coordkit implements the orchestration primitives migrated from
// open-multi-agent-main (TS): structured output extraction/validation with a
// single retry, the inter-agent message bus, the title-token dependency DAG,
// and the coordinator runTeam-style decomposition → concurrent dispatch →
// synthesis loop.
//
// This package is deliberately self-contained: it depends only on the standard
// library and github.com/google/uuid so it can compile and be tested in
// isolation, independent of the wider CyberStrikeAI config/security stack that
// other parallel sessions are editing.
package coordkit

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrExtractJSON is returned by extractJSON when no valid JSON can be located.
var ErrExtractJSON = errors.New("no valid JSON could be extracted from output")

// extractJSON attempts to parse a JSON value out of an LLM's raw text output.
//
// Migrated from open-multi-agent-main src/agent/structured-output.ts extractJSON.
// Strategies, tried in order:
//  1. The whole trimmed string is valid JSON.
//  2. A ```json fenced block.
//  3. A bare ``` fenced block.
//  4. The first '{' to the last '}' (object).
//  5. The first '[' to the last ']' (array).
//
// Each strategy's failure falls through to the next; all failures yield
// ErrExtractJSON. This mirrors the reference project's tolerance for models
// that wrap JSON in prose or fences despite the prompt asking otherwise.
func extractJSON(raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ErrExtractJSON
	}

	// Case 1: direct parse.
	if v, ok := tryParseJSON(trimmed); ok {
		return v, nil
	}

	// Case 2a: ```json fenced block.
	if fence := extractFencedCodeBlock(trimmed, "json"); fence != "" {
		if v, ok := tryParseJSON(fence); ok {
			return v, nil
		}
	}

	// Case 2b: bare ``` fenced block.
	if fence := extractFencedCodeBlock(trimmed, ""); fence != "" {
		if v, ok := tryParseJSON(fence); ok {
			return v, nil
		}
	}

	// Case 3: first '{' to last '}' (object).
	if obj, ok := sliceBalanced(trimmed, '{', '}'); ok {
		if v, ok := tryParseJSON(obj); ok {
			return v, nil
		}
	}

	// Case 3b: first '[' to last ']' (array).
	if arr, ok := sliceBalanced(trimmed, '[', ']'); ok {
		if v, ok := tryParseJSON(arr); ok {
			return v, nil
		}
	}

	return nil, ErrExtractJSON
}

// extractFencedCodeBlock returns the inner text of the first code fence whose
// info string equals lang (case-insensitive). When lang is empty, any bare
// ``` fence is matched. Returns "" when no fence is found.
//
// A fence is a line beginning with three backticks; the info string is the
// token immediately following the opening backticks (e.g. "json" in ```json).
// The reference project uses a single regex; we avoid regex to keep the
// dependency surface minimal and the behavior explicit and testable.
func extractFencedCodeBlock(s, lang string) string {
	target := strings.ToLower(strings.TrimSpace(lang))
	const fence = "```"
	inFence := false
	infoMatched := false
	var inner []string

	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, fence) {
			if inFence && infoMatched {
				inner = append(inner, ln)
			}
			continue
		}
		// This line opens or closes a fence.
		if !inFence {
			info := strings.TrimSpace(strings.TrimPrefix(t, fence))
			infoLang := info
			if sp := strings.IndexByte(info, ' '); sp >= 0 {
				infoLang = strings.TrimSpace(info[:sp])
			}
			infoLang = strings.ToLower(infoLang)
			if target == "" || infoLang == target {
				inFence = true
				infoMatched = true
			}
			continue
		}
		// Closing fence of a matched block.
		if infoMatched {
			return strings.Join(inner, "\n")
		}
		inFence = false
		infoMatched = false
		inner = nil
	}
	return ""
}

// sliceBalanced returns the substring from the first occurrence of open to the
// last occurrence of close (inclusive), when both exist and close follows open.
func sliceBalanced(s string, open, close byte) (string, bool) {
	start := strings.IndexByte(s, open)
	end := strings.LastIndexByte(s, close)
	if start < 0 || end < 0 || end <= start {
		return "", false
	}
	return s[start : end+1], true
}

// tryParseJSON is a thin wrapper over encoding/json that reports success via
// the ok return rather than by error value, to keep the call sites readable.
func tryParseJSON(s string) (any, bool) {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err != nil {
		return nil, false
	}
	return v, true
}

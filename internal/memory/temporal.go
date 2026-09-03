package memory

import (
	"sort"
	"strings"
	"time"
)

// TemporalIntent classifies the temporal meaning of a query so the temporal
// scorer can match it against each instance's dated metadata. Mirrors the
// platform's "query temporal intent is classified without an extra LLM call"
// (mem0 docs/core-concepts/memory-evaluation.mdx:52).
//
// The classification is keyword-driven (no LLM), so it is deterministic and
// unit-testable.
type TemporalIntent int

const (
	TemporalIntentNone    TemporalIntent = iota // no temporal signal
	TemporalIntentCurrent                       // "now", "current", "today"
	TemporalIntentPast                          // "yesterday", "last week", "previously", "was"
	TemporalIntentFuture                        // "tomorrow", "next week", "planned", "will"
	TemporalIntentAtDate                        // a specific date mentioned in the query
)

// TemporalScore is the per-instance additive temporal nudge. Per mem0 platform
// docs (memory-evaluation.mdx:54), it is additive and semantic relevance always
// dominates; it never filters candidates out. This struct is produced by
// TemporalScoreInstance for debugging/explanation.
type TemporalScore struct {
	// Delta is the additive nudge in [-0.25, +0.25]. Positive nudges toward
	// current/future alignment; negative nudges away. Zero means no temporal
	// signal in either the query or the instance.
	Delta float64
	// Reason is a short human-readable justification for evals.
	Reason string
}

// MaxTemporalDelta bounds the additive nudge so temporal never dominates
// semantic + BM25. mem0 platform: "semantic relevance always dominates."
const MaxTemporalDelta = 0.25

// ClassifyTemporalIntent returns the temporal intent of a query via keyword
// matching. The order of checks matters: explicit date > future > past > current.
//
// This is the pure-Go port of the platform's intent classifier. There is no
// LLM call — the platform is explicit about this for cost reasons.
func ClassifyTemporalIntent(query string) TemporalIntent {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return TemporalIntentNone
	}
	// Explicit date in query → TemporalIntentAtDate.
	if dateFromQuery(q) != "" {
		return TemporalIntentAtDate
	}
	// Future markers checked before past because "next" can appear with past words.
	futureMarkers := []string{"tomorrow", "next week", "next month", "planned", "will ", "upcoming", "scheduled"}
	for _, m := range futureMarkers {
		if strings.Contains(q, m) {
			return TemporalIntentFuture
		}
	}
	pastMarkers := []string{"yesterday", "last week", "last month", "previously", " was ", "before", "history"}
	for _, m := range pastMarkers {
		if strings.Contains(q, m) {
			return TemporalIntentPast
		}
	}
	currentMarkers := []string{"now", "current", "today", "present", "active"}
	for _, m := range currentMarkers {
		if strings.Contains(q, m) {
			return TemporalIntentCurrent
		}
	}
	return TemporalIntentNone
}

// dateFromQuery extracts a YYYY-MM-DD date token from a lowercase query string.
// Returns "" if none present. We only handle ISO dates in the query (the
// platform's reference_date convention); natural-language dates like "Aug 15"
// would need an LLM, which is deliberately out of scope for the pure classifier.
func dateFromQuery(q string) string {
	// Scan for 10-char YYYY-MM-DD tokens.
	for i := 0; i+10 <= len(q); i++ {
		tok := q[i : i+10]
		if isISODate(tok) {
			return tok
		}
	}
	return ""
}

func isISODate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		switch i {
		case 4, 7:
			continue
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	y, m, d := s[0:4], s[5:7], s[8:10]
	if y < "1900" || m < "01" || m > "12" || d < "01" || d > "31" {
		return false
	}
	return true
}

// TemporalScoreInstance computes the additive temporal nudge for one instance
// against a classified query intent. The semantics follow mem0 platform docs
// (memory-decay.mdx:46,157; memory-evaluation.mdx:31):
//
//   - A future event_date → full positive activation (the instance is the
//     "current truth" for a planned event).
//   - A past event_date → decay from when the event happened; older events
//     decay more, bounded so they never fully vanish.
//   - Query intent matching the instance's temporal direction yields a small
//     positive nudge; mismatch yields a small negative nudge.
//   - For AtDate intent, the instance's event_date must actually equal the
//     queryDate (extracted from the query by the caller); a mismatched date
//     gets a negative nudge, not the full boost.
//
// The delta is clamped to [-MaxTemporalDelta, +MaxTemporalDelta].
//
// queryDate is the YYYY-MM-DD extracted from the query ("" if none). It is
// the single source of truth for the AtDate branch — passing it explicitly
// avoids the earlier bug where the function could not see the query.
func TemporalScoreInstance(inst *FactInstance, intent TemporalIntent, queryDate string, now time.Time) TemporalScore {
	if inst == nil {
		return TemporalScore{}
	}

	// Superseded instances get a strong negative nudge — they are history, not
	// current truth. The platform's latest_only mode filters them entirely; in
	// additive scoring we instead demote them so they surface only when no
	// active instance matches.
	if inst.LifecycleState == LifecycleSuperseded {
		return TemporalScore{Delta: -MaxTemporalDelta, Reason: "superseded (history)"}
	}
	if inst.LifecycleState == LifecycleMerged {
		return TemporalScore{Delta: -MaxTemporalDelta / 2, Reason: "merged duplicate"}
	}

	// No temporal signal on either side → zero nudge.
	instDate, hasDate := parseEventDate(inst.EventDate)
	intentNone := intent == TemporalIntentNone
	if intentNone && !hasDate {
		return TemporalScore{Delta: 0, Reason: "no temporal signal"}
	}

	// Current/active instance with no query intent and no event date: tiny
	// positive nudge so active facts win ties over undated ones.
	if intent == TemporalIntentCurrent && !hasDate {
		return TemporalScore{Delta: MaxTemporalDelta * 0.4, Reason: "query=current, instance active undated"}
	}

	// Query is at-date: align to instance date match. The query date is
	// extracted from the query string by the caller (Service.Retrieve parses
	// it via dateFromQuery) and passed here explicitly — this is the single
	// source of truth for the AtDate branch.
	if intent == TemporalIntentAtDate {
		if !hasDate {
			// Instance undated but query names a specific date — slight
			// negative nudge (the instance is not the date the user asked about).
			return TemporalScore{Delta: -MaxTemporalDelta * 0.1, Reason: "query at-date but instance undated"}
		}
		qDate, qOK := parseEventDate(queryDate)
		if !qOK {
			// Classifier said AtDate but the query date failed to parse —
			// treat as undated-query: only the instance's own date matters.
			return TemporalScore{Delta: 0, Reason: "at-date intent but query date unparseable"}
		}
		if instDate.Equal(qDate) {
			return TemporalScore{Delta: MaxTemporalDelta, Reason: "query date matches instance event_date"}
		}
		// Mismatched date: small negative nudge so wrong-date instances sink
		// rather than float on a date-bearing query.
		daysOff := instDate.Sub(qDate).Hours() / 24.0
		if daysOff < 0 {
			daysOff = -daysOff
		}
		penalty := -MaxTemporalDelta * 0.2 * (1.0 - temporalActivation(-daysOff))
		if penalty < -MaxTemporalDelta*0.2 {
			penalty = -MaxTemporalDelta * 0.2
		}
		return TemporalScore{Delta: penalty, Reason: "query date does not match instance event_date"}
	}

	if !hasDate {
		// Instance undated but query has temporal intent — small nudge toward alignment.
		switch intent {
		case TemporalIntentCurrent:
			return TemporalScore{Delta: MaxTemporalDelta * 0.2, Reason: "query=current, instance undated"}
		case TemporalIntentFuture:
			return TemporalScore{Delta: -MaxTemporalDelta * 0.2, Reason: "query=future, instance undated"}
		case TemporalIntentPast:
			return TemporalScore{Delta: -MaxTemporalDelta * 0.1, Reason: "query=past, instance undated"}
		}
		return TemporalScore{}
	}

	// Instance has event_date: compute future vs past relative to now.
	daysOut := instDate.Sub(now).Hours() / 24.0

	// Future event_date: high positive activation that decays as it approaches now
	// and beyond into the past (platform: "future dates land near 1.0×, past
	// dates decay from when the event happened").
	activation := temporalActivation(daysOut)

	switch intent {
	case TemporalIntentFuture:
		// Query wants future; instance is future → aligned.
		if daysOut > 0 {
			return TemporalScore{Delta: MaxTemporalDelta * activation, Reason: "query=future, instance=future"}
		}
		// Instance is past but query wants future → misaligned.
		return TemporalScore{Delta: -MaxTemporalDelta * (1 - activation) * 0.5, Reason: "query=future, instance=past"}
	case TemporalIntentPast:
		if daysOut <= 0 {
			return TemporalScore{Delta: MaxTemporalDelta * activation, Reason: "query=past, instance=past"}
		}
		return TemporalScore{Delta: -MaxTemporalDelta * (1 - activation) * 0.5, Reason: "query=past, instance=future"}
	case TemporalIntentCurrent:
		// Current truth: instance most recently relevant gets positive nudge.
		return TemporalScore{Delta: MaxTemporalDelta * activation * 0.5, Reason: "query=current"}
	}

	return TemporalScore{Delta: 0, Reason: "no match"}
}

// temporalActivation returns the [0,1] activation for an event_date that is
// `daysOut` days from now. Future (daysOut>0) lands near 1.0; past decays with
// a soft exponential, bounded at 0.1 so history never fully vanishes.
//
// Mirrors mem0 platform memory-decay.mdx:46,157: "a future event_date gets a
// fresh/full activation, a past one decays from when the event happened."
func temporalActivation(daysOut float64) float64 {
	if daysOut >= 0 {
		// Future: full activation, lightly decaying as it's far away.
		// Within 7 days → 1.0; beyond → gentle decay but never below 0.5.
		if daysOut <= 7 {
			return 1.0
		}
		// 0.5 + 0.5*exp(-0.02*(daysOut-7)) — slow decay for far-future plans.
		return 0.5 + 0.5*mathExp(-0.02*(daysOut-7))
	}
	// Past: decay from the event date. 0.1 + 0.9*exp(-0.05*|daysOut|).
	daysPast := -daysOut
	return 0.1 + 0.9*mathExp(-0.05*daysPast)
}

// parseEventDate parses an ISO YYYY-MM-DD EventDate into a UTC time.Time.
// Returns (zero, false) when the field is empty or malformed.
func parseEventDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if !isISODate(s) {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// ApplyTemporalAdjustment adds each instance's temporal delta to its combined
// fusion score and re-sorts. The fusion score stays in [0,1] because the delta
// is clamped. This is the "additive temporal pass" the platform describes
// (memory-evaluation.mdx:54: "temporal score is additive and semantic
// relevance always dominates").
//
// queryDate is the YYYY-MM-DD extracted from the query ("" if none) and is
// forwarded to TemporalScoreInstance so the AtDate branch can match dates.
func ApplyTemporalAdjustment(results []FusionResult, intent TemporalIntent, queryDate string, now time.Time) []FusionResult {
	if len(results) == 0 {
		return results
	}
	for i := range results {
		ts := TemporalScoreInstance(results[i].Instance, intent, queryDate, now)
		adjusted := results[i].CombinedScore + ts.Delta
		if adjusted < 0 {
			adjusted = 0
		}
		if adjusted > 1 {
			adjusted = 1
		}
		results[i].CombinedScore = adjusted
		results[i].Explanation.TemporalReason = ts.Reason
		results[i].Explanation.TemporalDelta = ts.Delta
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].CombinedScore != results[j].CombinedScore {
			return results[i].CombinedScore > results[j].CombinedScore
		}
		return results[i].Instance.ID < results[j].Instance.ID
	})
	return results
}

// mathExp is a thin wrapper so this file has no direct math import in its
// top-level uses (keeping the package easy to grep for math dependencies).
// It is exactly math.Exp.
func mathExp(x float64) float64 {
	// imported inline via a package-level alias to avoid a second import block
	// entry in this file — see exp.go for the actual math.Exp binding.
	return mathExpFn(x)
}

// BackfillTemporalAttribs derives structured temporal attributes from a fact's
// EventDate (the platform's structured_attributes: year, month, day_of_week,
// is_weekend, quarter, week_of_year). This is the pure-Go port of mem0
// architecture.md:170-178.
type TemporalAttribs struct {
	Year       int
	Month      int
	DayOfWeek  int // Sunday=0
	IsWeekend  bool
	Quarter    int // 1..4
	WeekOfYear int // 1..53 (ISO week)
}

func BackfillTemporalAttribs(eventDate string) (TemporalAttribs, bool) {
	t, ok := parseEventDate(eventDate)
	if !ok {
		return TemporalAttribs{}, false
	}
	_, week := t.ISOWeek()
	return TemporalAttribs{
		Year:       t.Year(),
		Month:      int(t.Month()),
		DayOfWeek:  int(t.Weekday()),
		IsWeekend:  t.Weekday() == time.Saturday || t.Weekday() == time.Sunday,
		Quarter:    (int(t.Month())-1)/3 + 1,
		WeekOfYear: week,
	}, true
}

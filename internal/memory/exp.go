package memory

import "math"

// mathExpFn is the single binding to math.Exp used by temporal.go. Keeping it
// in a one-line file means temporal.go stays math-free at the top, which makes
// the package's math surface trivially auditable.
func mathExpFn(x float64) float64 { return math.Exp(x) }

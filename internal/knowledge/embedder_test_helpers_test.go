package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// jsonDecodeBody decodes the request JSON body into v.
func jsonDecodeBody(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// jsonEncode writes v as JSON to w.
func jsonEncode(w http.ResponseWriter, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// contextWithCancel returns a cancellable context derived from the test context.
func contextWithCancel(t *testing.T) (ctx context.Context, cancel context.CancelFunc) {
	t.Helper()
	return context.WithCancel(t.Context())
}

// ensure time import is used in helpers file context (fixedTestTime lives in
// knowledge_test_helpers_test.go; keep this file self-contained).
var _ = time.Now

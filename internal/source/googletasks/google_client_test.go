package googletasks

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/api/googleapi"
)

// TestNewGoogleClientRejectsNilHTTP guards the boundary: a nil
// *http.Client would leave the SDK with no transport, so we refuse it
// up front with a typed error instead of letting the upstream panic at
// first call.
func TestNewGoogleClientRejectsNilHTTP(t *testing.T) {
	t.Parallel()

	if _, err := NewGoogleClient(context.Background(), nil); err == nil {
		t.Fatalf("NewGoogleClient(nil http): want error, got nil")
	}
}

// TestTranslateErrorMapsUnauthorizedAndForbidden pins the 401/403 mapping
// AuthStatus relies on. Without this check, a revoked token would
// surface as a generic "auth status: googleapi error 401" instead of
// AuthRevoked, which is the only signal the dispatcher uses to skip
// dispatching against a dead source.
func TestTranslateErrorMapsUnauthorizedAndForbidden(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		err := translateError(&googleapi.Error{Code: code, Message: "no"})
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("translateError(%d): got %v, want wraps ErrUnauthorized", code, err)
		}
	}
}

// TestTranslateErrorPassesThroughOtherCodes confirms the inverse: a 500
// or a non-API error must NOT be misclassified as ErrUnauthorized,
// otherwise a transient outage would push every account into the
// "revoked" bucket and force everyone to re-run setup.
func TestTranslateErrorPassesThroughOtherCodes(t *testing.T) {
	t.Parallel()

	err := translateError(&googleapi.Error{Code: http.StatusInternalServerError, Message: "boom"})
	if errors.Is(err, ErrUnauthorized) {
		t.Errorf("500 should not be ErrUnauthorized, got %v", err)
	}
	if errors.Is(err, ErrUpstreamTaskMissing) {
		t.Errorf("500 should not be ErrUpstreamTaskMissing, got %v", err)
	}
	plain := errors.New("network broken")
	if got := translateError(plain); !errors.Is(got, plain) {
		t.Errorf("non-googleapi err should pass through, got %v", got)
	}
	if got := translateError(nil); got != nil {
		t.Errorf("translateError(nil) = %v, want nil", got)
	}
}

// TestTranslateErrorMaps404ToUpstreamMissing pins the TOCTOU translation
// path: when the upstream answers 404 between findTaskList and the
// patch / delete, the Plugin needs the typed sentinel so it can
// re-translate to ErrTaskNotFound for callers.
func TestTranslateErrorMaps404ToUpstreamMissing(t *testing.T) {
	t.Parallel()

	err := translateError(&googleapi.Error{Code: http.StatusNotFound, Message: "not found"})
	if !errors.Is(err, ErrUpstreamTaskMissing) {
		t.Fatalf("translateError(404): got %v, want wraps ErrUpstreamTaskMissing", err)
	}
}

// TestTruncateMessageBoundsPayload guards the security fix: an upstream
// error with a giant reflected payload must not enter the error chain
// verbatim. We assert both the cap and the visible suffix so a future
// edit that loosens the limit goes red.
func TestTruncateMessageBoundsPayload(t *testing.T) {
	t.Parallel()

	huge := make([]byte, 4096)
	for i := range huge {
		huge[i] = 'A'
	}
	got := truncateMessage(string(huge))
	if len(got) > 200 { // limit (120) + " ...(truncated)" suffix headroom
		t.Errorf("truncateMessage did not cap length: len=%d", len(got))
	}
	if got == string(huge) {
		t.Errorf("truncateMessage returned the input verbatim")
	}
	short := "ok"
	if truncateMessage(short) != short {
		t.Errorf("short message should pass through unchanged")
	}
}

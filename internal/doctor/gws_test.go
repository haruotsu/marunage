package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeGWSProbe struct {
	ok      bool
	account string
	err     error
}

func (f fakeGWSProbe) Authenticated(context.Context) (bool, string, error) {
	return f.ok, f.account, f.err
}

func gwsRunner() fakeRunner {
	return fakeRunner{
		present:  map[string]string{"gws": "/opt/homebrew/bin/gws"},
		versions: map[string]string{"gws": "gws 0.9.0\n"},
	}
}

func TestProbeGWS_AuthenticatedIsOK(t *testing.T) {
	out := probeGWS(context.Background(), Inputs{
		Runner: gwsRunner(),
		GWS:    fakeGWSProbe{ok: true, account: "haruotsu@haruotsu.jp"},
	})
	if !out.OK {
		t.Fatalf("OK = false; want true. detail=%q", out.Detail)
	}
	if !strings.Contains(out.Detail, "authenticated") || !strings.Contains(out.Detail, "haruotsu@haruotsu.jp") {
		t.Errorf("detail = %q; want it to mention authenticated + account", out.Detail)
	}
}

func TestProbeGWS_NotAuthenticatedFailsWithLoginHint(t *testing.T) {
	out := probeGWS(context.Background(), Inputs{
		Runner: gwsRunner(),
		GWS:    fakeGWSProbe{ok: false},
	})
	if out.OK {
		t.Fatalf("OK = true; want false when gws is not logged in")
	}
	if !strings.Contains(out.Hint, "gws auth login") || !strings.Contains(out.Hint, "gmail,calendar,tasks") {
		t.Errorf("hint = %q; want the scope-narrowed login command", out.Hint)
	}
}

func TestProbeGWS_NilProbeFallsBackToBinaryCheck(t *testing.T) {
	out := probeGWS(context.Background(), Inputs{Runner: gwsRunner()})
	if !out.OK {
		t.Fatalf("OK = false; want true (binary present, no auth probe wired). detail=%q", out.Detail)
	}
	if strings.Contains(out.Detail, "authenticated") {
		t.Errorf("detail = %q; should not assert auth state without a probe", out.Detail)
	}
}

func TestProbeGWS_MissingBinaryFailsBeforeAuth(t *testing.T) {
	out := probeGWS(context.Background(), Inputs{
		Runner: fakeRunner{present: map[string]string{}},
		GWS:    fakeGWSProbe{ok: true}, // never consulted
	})
	if out.OK {
		t.Fatalf("OK = true; want false when gws binary is absent")
	}
}

func TestProbeGWS_ProbeErrorKeepsBinaryOKButNotesIt(t *testing.T) {
	out := probeGWS(context.Background(), Inputs{
		Runner: gwsRunner(),
		GWS:    fakeGWSProbe{err: errors.New("gws blew up")},
	})
	if !out.OK {
		t.Fatalf("OK = false; want true (binary present; auth merely unverifiable)")
	}
	if !strings.Contains(out.Detail, "could not be verified") {
		t.Errorf("detail = %q; want it to note the auth state is unknown", out.Detail)
	}
}

package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CLIGWSAuthProbe implements GWSAuthProbe by running `gws auth status` and
// reading the JSON it prints. gws reports "auth_method": "none" before a
// login and a concrete method (e.g. "oauth2") afterwards, so the absence of a
// credential is detectable without attempting a real API call.
type CLIGWSAuthProbe struct{}

// gwsAuthStatus mirrors the subset of `gws auth status` JSON we consume.
type gwsAuthStatus struct {
	AuthMethod string `json:"auth_method"`
	Account    string `json:"account"`
}

// Authenticated runs `gws auth status` and reports whether a credential is
// present. A missing binary or a non-zero exit is surfaced as an error so the
// caller can distinguish "could not probe" from "probed, not logged in".
func (CLIGWSAuthProbe) Authenticated(ctx context.Context) (bool, string, error) {
	out, err := exec.CommandContext(ctx, "gws", "auth", "status").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return false, "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return false, "", err
	}
	// gws may prefix the JSON with a human line (e.g. "Using keyring backend:
	// keyring"); start parsing at the first '{'.
	raw := out
	if i := strings.IndexByte(string(out), '{'); i > 0 {
		raw = out[i:]
	}
	var st gwsAuthStatus
	if jerr := json.Unmarshal(raw, &st); jerr != nil {
		return false, "", fmt.Errorf("gws auth status: parse json: %w", jerr)
	}
	authed := st.AuthMethod != "" && st.AuthMethod != "none"
	return authed, st.Account, nil
}

package secrets_test

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
)

// TestReadPassphraseFromTTYNoTerminal pins the headless-failure mode:
// when stdin is not a terminal, the prompter must short-circuit with
// ErrPassphraseRequired rather than calling term.ReadPassword on a
// non-TTY descriptor (which would either return an error with no
// useful diagnostic, or block on a read that never completes
// depending on the kernel/glibc combination).
//
// Regression guard: the production readPassphraseFromTTY checks
// term.IsTerminal up front; without this test that branch is
// completely uncovered, so a refactor that moved the check elsewhere
// would land a hung daemon in production.
func TestReadPassphraseFromTTYNoTerminal(t *testing.T) {
	restore := secrets.SetTTYHooksForTest(
		func(_ int) bool { return false }, // not a terminal
		func(_ int) ([]byte, error) {
			t.Fatal("ReadPassword must not be called when IsTerminal is false")
			return nil, nil
		},
	)
	defer restore()

	_, err := secrets.ReadPassphraseFromTTYForTest(false)
	if err == nil {
		t.Fatal("readPassphraseFromTTY without TTY = nil; want ErrPassphraseRequired")
	}
	if !errors.Is(err, secrets.ErrPassphraseRequired) {
		t.Errorf("error = %v; want errors.Is(..., ErrPassphraseRequired)", err)
	}
}

// TestReadPassphraseFromTTYConfirmMismatch pins the typed-error
// contract for the confirm prompt: when the second read does not
// match the first, the prompter must return ErrPassphraseMismatch so
// the CLI can re-prompt rather than encrypting the vault under a
// passphrase the user cannot reproduce. The sentinel is exported and
// the implementation branches on it; without this test the path is
// untested and a refactor could regress it silently.
func TestReadPassphraseFromTTYConfirmMismatch(t *testing.T) {
	calls := 0
	restore := secrets.SetTTYHooksForTest(
		func(_ int) bool { return true },
		func(_ int) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("first-pass"), nil
			}
			return []byte("second-pass"), nil
		},
	)
	defer restore()

	_, err := secrets.ReadPassphraseFromTTYForTest(true)
	if err == nil {
		t.Fatal("mismatched confirm = nil; want ErrPassphraseMismatch")
	}
	if !errors.Is(err, secrets.ErrPassphraseMismatch) {
		t.Errorf("error = %v; want errors.Is(..., ErrPassphraseMismatch)", err)
	}
	if calls != 2 {
		t.Errorf("ReadPassword call count = %d; want 2 (one prompt + one confirm)", calls)
	}
}

// TestReadPassphraseFromTTYConfirmMatches pins the happy path of the
// confirm prompt: when both reads match, the prompter returns the
// passphrase. Without this test the success branch of the confirm
// path is uncovered and the equality check could regress to "always
// mismatch" without any signal.
func TestReadPassphraseFromTTYConfirmMatches(t *testing.T) {
	restore := secrets.SetTTYHooksForTest(
		func(_ int) bool { return true },
		func(_ int) ([]byte, error) {
			return []byte("matching-pass"), nil
		},
	)
	defer restore()

	got, err := secrets.ReadPassphraseFromTTYForTest(true)
	if err != nil {
		t.Fatalf("matching confirm: %v; want nil", err)
	}
	if got != "matching-pass" {
		t.Errorf("returned passphrase = %q; want %q", got, "matching-pass")
	}
}

// TestReadPassphraseFromTTYNoConfirm pins the no-confirm branch (used
// when decrypting an existing vault — age's MAC is the integrity
// check, so a single read is enough). Locks in that the prompter
// reads exactly once when needConfirm=false, no second prompt sneaks
// in even if the implementation is later refactored to share code
// with the confirm path.
func TestReadPassphraseFromTTYNoConfirm(t *testing.T) {
	calls := 0
	restore := secrets.SetTTYHooksForTest(
		func(_ int) bool { return true },
		func(_ int) ([]byte, error) {
			calls++
			return []byte("single-pass"), nil
		},
	)
	defer restore()

	got, err := secrets.ReadPassphraseFromTTYForTest(false)
	if err != nil {
		t.Fatalf("no-confirm: %v; want nil", err)
	}
	if got != "single-pass" {
		t.Errorf("returned passphrase = %q; want %q", got, "single-pass")
	}
	if calls != 1 {
		t.Errorf("ReadPassword call count = %d; want 1 (no confirm)", calls)
	}
}

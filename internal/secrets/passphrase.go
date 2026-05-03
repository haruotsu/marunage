package secrets

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/term"
)

// readPassphraseFromTTY is the production passphrasePrompter installed
// in ttyPassphrasePrompter. It echo-suppresses the terminal so the
// passphrase never lands in shell scrollback, and on first-time vault
// creation (needConfirm=true) re-prompts so a typo cannot encrypt a
// vault under a passphrase the user cannot reproduce.
//
// On a non-TTY stdin (CI without MARUNAGE_AGE_PASSPHRASE, a Docker
// container piping nothing into the daemon, etc.) we return
// ErrPassphraseRequired immediately rather than blocking on a read that
// would never succeed. Callers — typically `marunage setup` — surface
// that as actionable guidance ("set MARUNAGE_AGE_PASSPHRASE or run from
// a TTY") instead of a hung process.
func readPassphraseFromTTY(needConfirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", ErrPassphraseRequired
	}
	first, err := promptOnce(fd, "Enter passphrase for ~/.marunage/secrets.age: ")
	if err != nil {
		return "", err
	}
	if !needConfirm {
		return string(first), nil
	}
	second, err := promptOnce(fd, "Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if !bytes.Equal(first, second) {
		return "", ErrPassphraseMismatch
	}
	return string(first), nil
}

// promptOnce writes the prompt to stderr (so it is visible even when
// stdout is being captured into a pipe) and reads one line of input
// with terminal echo suppressed. The trailing newline is written
// manually because term.ReadPassword swallows the user's Enter.
func promptOnce(fd int, prompt string) ([]byte, error) {
	if _, err := fmt.Fprint(os.Stderr, prompt); err != nil {
		return nil, fmt.Errorf("write prompt: %w", err)
	}
	pw, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read passphrase: %w", err)
	}
	return pw, nil
}

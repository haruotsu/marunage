package secrets

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// passStorePrefixDir is the top-level directory inside the pass store where
// marunage keeps its secrets (i.e. ~/.password-store/marunage/<name>).
const passStorePrefixDir = "marunage"

// passBackend wraps the UNIX pass(1) password store. Secrets live under
// marunage/<name> in the store, GPG-encrypted, and are managed entirely
// through the pass(1) binary (except List, which reads the store directory
// directly to avoid spawning a process per entry).
type passBackend struct {
	storeDir string // ~/.password-store or $PASSWORD_STORE_DIR; overridable for tests
	runCmd   func(stdin io.Reader, name string, args ...string) ([]byte, error)
}

// probePassAvailable returns nil when the pass binary is present in PATH.
func probePassAvailable() error {
	_, err := exec.LookPath("pass")
	return err
}

func newPassBackend(cfg Config) (Store, error) {
	if err := probePassAvailable(); err != nil {
		return nil, ErrUnsupported
	}
	storeDir := cfg.PassStoreDir
	if storeDir == "" {
		if v := os.Getenv("PASSWORD_STORE_DIR"); v != "" {
			storeDir = v
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolve home dir: %w", err)
			}
			storeDir = filepath.Join(home, ".password-store")
		}
	}
	return &passBackend{
		storeDir: storeDir,
		runCmd:   passCmdRunner,
	}, nil
}

// passCmdRunner is the production implementation of passBackend.runCmd.
// It wraps the stderr from *exec.ExitError into the returned error so that
// isPassNotFound can do a plain string search rather than type-asserting
// *exec.ExitError at every call site.
// LC_ALL=C forces English output from pass(1) so that isPassNotFound's
// string match is not broken by locale-specific error messages.
func passCmdRunner(stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return out, err
	}
	return out, nil
}

func (p *passBackend) Backend() string { return "pass" }

func (p *passBackend) Set(name, value string) error {
	if err := validateName(name); err != nil {
		return err
	}
	// -f forces overwrite of an existing entry without an interactive prompt,
	// satisfying the Store.Set contract of "overwriting any existing entry".
	_, err := p.runCmd(strings.NewReader(value), "pass", "insert", "-m", "-f", passStorePrefixDir+"/"+name)
	if err != nil {
		return fmt.Errorf("pass insert %q: %w", name, err)
	}
	return nil
}

func (p *passBackend) Get(name string) (string, bool, error) {
	if err := validateName(name); err != nil {
		return "", false, err
	}
	out, err := p.runCmd(nil, "pass", "show", passStorePrefixDir+"/"+name)
	if err != nil {
		if isPassNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("pass show %q: %w", name, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	if scanner.Scan() {
		return scanner.Text(), true, nil
	}
	return "", true, nil
}

func (p *passBackend) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	_, err := p.runCmd(nil, "pass", "rm", "-f", passStorePrefixDir+"/"+name)
	if err != nil {
		if isPassNotFound(err) {
			return nil
		}
		return fmt.Errorf("pass rm %q: %w", name, err)
	}
	return nil
}

func (p *passBackend) List() ([]string, error) {
	dir := filepath.Join(p.storeDir, passStorePrefixDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if !strings.HasSuffix(fname, ".gpg") {
			continue
		}
		out = append(out, strings.TrimSuffix(fname, ".gpg"))
	}
	sort.Strings(out)
	return out, nil
}

// isPassNotFound reports whether err indicates that the entry does not exist
// in the pass store. passCmdRunner embeds stderr into the error message, so a
// plain string search is sufficient; mock runners in tests can return an error
// containing the same fragment.
func isPassNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is not in the password store")
}

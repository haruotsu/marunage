package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"filippo.io/age"
)

// ageBackend persists every secret into ~/.marunage/secrets.age, a single
// age-encrypted file decrypted with a user-supplied passphrase. It is the
// "no GUI, no pass" answer in the auto-select chain (docs/requirement.md
// "シークレット保存 — クロスプラットフォーム前提"): keyring needs a desktop
// session, pass needs the gpg/pass binaries, but age ships in-tree as pure
// Go and works on a bare server as long as someone can type a passphrase
// once (or feed it via MARUNAGE_AGE_PASSPHRASE).
//
// Layout choice — one combined vault file rather than per-secret files
// like fileBackend: scrypt-based age encryption is expensive (~hundreds of
// ms per derivation), so amortising one decrypt-then-mutate-then-encrypt
// over many entries is much cheaper than one decryption per Get. It also
// keeps the on-disk surface to a single 0600 file, which is easier to
// back up and reason about than a directory of per-secret ciphertexts.
type ageBackend struct {
	path          string
	passphraseEnv string

	mu     sync.Mutex
	cached string // process-local cache; "" until acquired
}

// secretsAgeFileName is the on-disk filename inside ~/.marunage. Pinned
// here so the persistence-across-Open test and the encrypt/decrypt path
// agree on a single source of truth.
const secretsAgeFileName = "secrets.age"

// AgePassphraseEnvDefault is the documented env var the age backend
// consults before falling back to a TTY prompt. Exported so callers (and
// tests) can refer to it without hard-coding the string.
const AgePassphraseEnvDefault = "MARUNAGE_AGE_PASSPHRASE"

// ageScryptLogN is the scrypt work factor passed to
// (*age.ScryptRecipient).SetWorkFactor. age's upstream default is 18
// which yields a multi-second derivation on commodity hardware; that is
// painful for a CLI that re-encrypts the vault on every Set. logN=15
// keeps derivations under ~200ms while still gating brute-force at >32k
// iterations. Tests override via SetAgeScryptLogNForTest to keep the
// suite fast under -race -count=2.
var ageScryptLogN = 15

// ttyPassphrasePrompter is the function the age backend calls when the
// passphrase is not already cached or supplied via env. Stored as a
// package var (rather than a constant or a method) so tests can swap in
// a deterministic prompter without forking the production code path.
var ttyPassphrasePrompter passphrasePrompter = readPassphraseFromTTY

// passphrasePrompter abstracts "ask the user for a passphrase". The
// boolean signals whether the caller wants the second confirmation read
// (true on first-time vault creation, false when decrypting an existing
// vault).
type passphrasePrompter func(needConfirm bool) (string, error)

// ErrPassphraseRequired is returned when no passphrase is available:
// MARUNAGE_AGE_PASSPHRASE is unset AND stdin is not a TTY (so prompting
// would block forever). Callers — typically `marunage setup` — surface
// this with a hint to either run interactively or set the env var.
var ErrPassphraseRequired = errors.New("secrets/age: passphrase required (set MARUNAGE_AGE_PASSPHRASE or run from a TTY)")

// ErrPassphraseMismatch is returned when the confirmation prompt during
// first-time vault creation does not match the initial entry. Reported
// as a typed sentinel so the CLI can re-prompt rather than treating it
// as a fatal error.
var ErrPassphraseMismatch = errors.New("secrets/age: passphrase confirmation did not match")

// ErrPassphraseIncorrect wraps age's "no identity matched any of the
// recipients" error when reading an existing vault: it tells the user
// the file exists but the supplied passphrase cannot decrypt it. Kept
// distinct from ErrPassphraseRequired so callers can branch on whether
// to prompt again or abort.
var ErrPassphraseIncorrect = errors.New("secrets/age: passphrase did not decrypt secrets file")

// processPassphraseCache memoises passphrases per vault path so a second
// secrets.Open in the same process reuses the first prompt. Keyed by
// absolute file path because two Configs that resolve to the same vault
// (e.g. HomeDir override during a test) should share state, but two
// different test temp dirs must not.
var (
	processPassphraseCacheMu sync.Mutex
	processPassphraseCache   = map[string]string{}
)

func getCachedPassphrase(path string) string {
	processPassphraseCacheMu.Lock()
	defer processPassphraseCacheMu.Unlock()
	return processPassphraseCache[path]
}

func setCachedPassphrase(path, value string) {
	processPassphraseCacheMu.Lock()
	defer processPassphraseCacheMu.Unlock()
	processPassphraseCache[path] = value
}

func newAgeBackend(cfg Config) (Store, error) {
	home := cfg.HomeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		home = h
	}
	// Mirror the file backend's parent-tighten pattern: ~/.marunage may
	// have been created with a wide umask by an earlier `marunage init`.
	// Even though secrets.age itself is 0600, leaving the umbrella dir
	// world-readable defeats defense-in-depth for the rest of the tree.
	marunageDir := filepath.Join(home, ".marunage")
	if err := os.MkdirAll(marunageDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", marunageDir, err)
	}
	if err := os.Chmod(marunageDir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", marunageDir, err)
	}
	return &ageBackend{
		path:          filepath.Join(marunageDir, secretsAgeFileName),
		passphraseEnv: cfg.AgePassphraseEnv,
	}, nil
}

func (a *ageBackend) Backend() string { return "age" }

// passphrase resolves the passphrase by walking the documented priority
// chain: in-struct cache → process cache (across Opens) → env var → TTY
// prompt. needConfirm is true only when we are about to create a brand-
// new vault, so the user types the passphrase twice; for decryption of
// an existing file we ask once and let age's MAC fail if it was wrong.
func (a *ageBackend) passphrase(needConfirm bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cached != "" {
		return a.cached, nil
	}
	if v := getCachedPassphrase(a.path); v != "" {
		a.cached = v
		return v, nil
	}
	envName := a.passphraseEnv
	if envName == "" {
		envName = AgePassphraseEnvDefault
	}
	if v := os.Getenv(envName); v != "" {
		a.cached = v
		setCachedPassphrase(a.path, v)
		return v, nil
	}
	if ttyPassphrasePrompter == nil {
		return "", ErrPassphraseRequired
	}
	p, err := ttyPassphrasePrompter(needConfirm)
	if err != nil {
		return "", err
	}
	a.cached = p
	setCachedPassphrase(a.path, p)
	return p, nil
}

func (a *ageBackend) Get(name string) (string, bool, error) {
	if err := validateName(name); err != nil {
		return "", false, err
	}
	if !a.fileExists() {
		// No vault on disk yet -> the secret cannot exist. Returning
		// without prompting for a passphrase keeps a fresh `marunage
		// setup --list` from blocking on a prompt the user did not ask
		// for.
		return "", false, nil
	}
	p, err := a.passphrase(false)
	if err != nil {
		return "", false, err
	}
	vault, err := a.loadVault(p)
	if err != nil {
		return "", false, err
	}
	v, ok := vault[name]
	return v, ok, nil
}

func (a *ageBackend) Set(name, value string) error {
	if err := validateName(name); err != nil {
		return err
	}
	isNew := !a.fileExists()
	p, err := a.passphrase(isNew)
	if err != nil {
		return err
	}
	var vault map[string]string
	if isNew {
		vault = map[string]string{}
	} else {
		vault, err = a.loadVault(p)
		if err != nil {
			return err
		}
	}
	vault[name] = value
	return a.saveVault(vault, p)
}

func (a *ageBackend) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !a.fileExists() {
		return nil
	}
	p, err := a.passphrase(false)
	if err != nil {
		return err
	}
	vault, err := a.loadVault(p)
	if err != nil {
		return err
	}
	if _, ok := vault[name]; !ok {
		// Idempotent delete: no rewrite of the file when there is
		// nothing to remove. Avoids spinning up scrypt for a no-op.
		return nil
	}
	delete(vault, name)
	return a.saveVault(vault, p)
}

func (a *ageBackend) List() ([]string, error) {
	if !a.fileExists() {
		return nil, nil
	}
	p, err := a.passphrase(false)
	if err != nil {
		return nil, err
	}
	vault, err := a.loadVault(p)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(vault))
	for k := range vault {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (a *ageBackend) fileExists() bool {
	_, err := os.Stat(a.path)
	return err == nil
}

// loadVault reads and decrypts secrets.age into the in-memory map. A
// missing file is treated as an empty vault rather than an error so
// callers (Get / List) can short-circuit cleanly on a fresh install.
func (a *ageBackend) loadVault(passphrase string) (map[string]string, error) {
	body, err := os.ReadFile(a.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", a.path, err)
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("scrypt identity: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(body), identity)
	if err != nil {
		// age returns a generic "no identity matched any of the
		// recipients" when the passphrase is wrong; surface the typed
		// sentinel so callers can distinguish "needs another prompt"
		// from "I/O error" without string-matching upstream messages.
		if strings.Contains(err.Error(), "no identity matched") {
			return nil, ErrPassphraseIncorrect
		}
		return nil, fmt.Errorf("age decrypt: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted body: %w", err)
	}
	if len(plaintext) == 0 {
		return map[string]string{}, nil
	}
	var vault map[string]string
	if err := json.Unmarshal(plaintext, &vault); err != nil {
		return nil, fmt.Errorf("parse vault: %w", err)
	}
	if vault == nil {
		vault = map[string]string{}
	}
	return vault, nil
}

// saveVault re-encrypts the entire vault and atomically renames it into
// place. The temp file lives in the same directory as the target so the
// rename is on the same filesystem (a cross-device rename would fall
// back to a copy and break the atomicity guarantee).
func (a *ageBackend) saveVault(vault map[string]string, passphrase string) error {
	body, err := json.Marshal(vault)
	if err != nil {
		return fmt.Errorf("marshal vault: %w", err)
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return fmt.Errorf("scrypt recipient: %w", err)
	}
	recipient.SetWorkFactor(ageScryptLogN)
	var encrypted bytes.Buffer
	w, err := age.Encrypt(&encrypted, recipient)
	if err != nil {
		return fmt.Errorf("age encrypt: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close encrypt: %w", err)
	}

	dir := filepath.Dir(a.path)
	tmp, err := os.CreateTemp(dir, secretsAgeFileName+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(encrypted.Bytes()); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanupTmp()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, a.path); err != nil {
		cleanupTmp()
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}

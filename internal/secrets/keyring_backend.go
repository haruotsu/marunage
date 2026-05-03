package secrets

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/zalando/go-keyring"
)

// keyringServiceName is the OS-keychain "service" string under which
// every marunage secret lives. macOS Keychain, freedesktop Secret
// Service, and Windows Credential Manager all key on this plus the
// account name (the source identifier the caller passes in).
const keyringServiceName = "marunage"

// keyringBackend wraps github.com/zalando/go-keyring so the OS-native
// secret store is the first thing auto-select tries. The library hides
// the per-OS API differences (macOS Keychain / freedesktop Secret
// Service / Windows Credential Manager) behind a flat Get/Set/Delete.
//
// go-keyring does not expose an enumeration API, so we maintain a small
// in-process index of names that have been Set or Get'd successfully.
// The index is best-effort: a name written by a previous process that
// this process never touched will not appear in List(). The same caveat
// is documented upstream and is acceptable because List() is used by
// `marunage setup --list` which iterates the configured sources, not
// the keychain itself.
type keyringBackend struct {
	mu    sync.Mutex
	known map[string]struct{}
}

func newKeyringBackend(_ Config) (Store, error) {
	// Probe the keychain by reading a sentinel name. A "secret not
	// found" reply is success — it proves the platform key store is
	// reachable. Any other error means the daemon is missing (Linux
	// without gnome-keyring, headless server, etc.) so we surface
	// ErrUnsupported and let auto-select move on to the next backend.
	_, err := keyring.Get(keyringServiceName, "__marunage_probe__")
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("%w: keyring probe failed: %v", ErrUnsupported, err)
	}
	return &keyringBackend{known: make(map[string]struct{})}, nil
}

func (k *keyringBackend) Backend() string { return "keyring" }

func (k *keyringBackend) Get(name string) (string, bool, error) {
	if err := validateName(name); err != nil {
		return "", false, err
	}
	v, err := keyring.Get(keyringServiceName, name)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("keyring get %q: %w", name, err)
	}
	k.remember(name)
	return v, true, nil
}

func (k *keyringBackend) Set(name, value string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := keyring.Set(keyringServiceName, name, value); err != nil {
		return fmt.Errorf("keyring set %q: %w", name, err)
	}
	k.remember(name)
	return nil
}

func (k *keyringBackend) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := keyring.Delete(keyringServiceName, name); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			k.forget(name)
			return nil
		}
		return fmt.Errorf("keyring delete %q: %w", name, err)
	}
	k.forget(name)
	return nil
}

func (k *keyringBackend) List() ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.known))
	for name := range k.known {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (k *keyringBackend) remember(name string) {
	k.mu.Lock()
	k.known[name] = struct{}{}
	k.mu.Unlock()
}

func (k *keyringBackend) forget(name string) {
	k.mu.Lock()
	delete(k.known, name)
	k.mu.Unlock()
}

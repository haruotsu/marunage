package secrets

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// envBackend reads tokens from process environment variables of the form
// MARUNAGE_<UPPER_NAME>_TOKEN. It is the documented fallback for CI /
// Docker / one-shot runs (`marunage setup --headless`) and is the only
// backend explicitly excluded from auto-select to keep workstation
// runs from silently picking it up; see secrets.go package doc.
type envBackend struct{}

func newEnvBackend(_ Config) (Store, error) {
	return envBackend{}, nil
}

func (envBackend) Backend() string { return "env" }

func (envBackend) Get(name string) (string, bool, error) {
	if err := validateName(name); err != nil {
		return "", false, err
	}
	v, ok := os.LookupEnv(envVarFor(name))
	if !ok || v == "" {
		// Treat empty MARUNAGE_FOO_TOKEN= as "not set" so a CI workflow
		// that scaffolds the variable name without a real value behaves
		// the same as a workflow that omits the variable entirely.
		return "", false, nil
	}
	return v, true, nil
}

func (envBackend) Set(_ string, _ string) error {
	return fmt.Errorf("env backend cannot persist secrets: %w", ErrReadOnly)
}

func (envBackend) Delete(_ string) error {
	return fmt.Errorf("env backend cannot persist secrets: %w", ErrReadOnly)
}

func (envBackend) List() ([]string, error) {
	const prefix = "MARUNAGE_"
	const suffix = "_TOKEN"
	var out []string
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
			continue
		}
		mid := key[len(prefix) : len(key)-len(suffix)]
		if mid == "" {
			continue
		}
		out = append(out, strings.ToLower(mid))
	}
	sort.Strings(out)
	return out, nil
}

// envVarFor maps a source name into the MARUNAGE_<UPPER>_TOKEN convention.
// We upper-case the name regardless of the caller's casing because Unix
// env var names are conventionally upper-case and this avoids a class of
// "I set MARUNAGE_Gmail_TOKEN, why doesn't it work" bugs.
func envVarFor(name string) string {
	return "MARUNAGE_" + strings.ToUpper(name) + "_TOKEN"
}

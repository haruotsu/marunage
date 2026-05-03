package secrets_test

// Test list (t_wada TDD - tick off as each turns green):
//
//   1. file backend: Set then Get returns the value; List includes the
//      name; Delete removes it; Get on missing returns ok=false, no error.
//   2. file backend: file is created with 0600, parent dir 0700.
//   3. env backend: Get reads MARUNAGE_FOO_TOKEN when name="foo";
//      Set/Delete return ErrReadOnly.
//   4. auto-select: when keyring + file are both available, keyring wins;
//      when only file is available, file is chosen and Backend()=="file".
//   5. auto-select: pass and age stubs return ErrUnsupported and are
//      skipped without error.
//   6. Open with explicit Backend="file" never tries keyring even if
//      keyring is available (deterministic override).
//   7. Open with explicit Backend="env" returns the env backend even
//      when no env var is set yet.
//   8. Open with unknown Backend value returns a validation error before
//      touching disk.
//   9. Set + Delete each emit exactly one audit line containing the
//      action and backend; the value never appears in audit output.
//  10. config.Validate rejects secrets.backend = "garbage" with a helpful
//      message; accepts each of auto/keyring/pass/age/file/env.
//      (Lives in internal/config; covered there.)
//  11. config Save/Load round-trip preserves secrets.backend.
//      (Lives in internal/config; covered there.)

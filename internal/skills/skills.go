package skills

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RequiredTriageSections enumerates the H2 headers `marunage setup
// --skills` insists on after writing the triage SKILL.md. The list is
// public so tests on the embedded bundle and a future Web UI surface
// can apply the same contract without re-deriving it.
var RequiredTriageSections = []string{"判定ロジック", "出力フォーマット"}

// triageSkillName is the on-disk directory name the validation step
// targets. We pin it in one place so renaming the embedded skill
// directory automatically propagates here.
const triageSkillName = "marunage-triage"

// SkillReport is the per-skill outcome row in InstallResult. The Old/New
// version pair powers `--check-updates`-style reporting; on a fresh
// install OldVersion is empty.
type SkillReport struct {
	Name       string
	OldVersion string
	NewVersion string
}

// InstallResult bins each handled skill into one of four buckets. A
// single Install call only ever populates the buckets that match its
// mode: e.g. --diff fills Diffs but never Installed.
type InstallResult struct {
	Installed []SkillReport
	Skipped   []SkillReport
	Updated   []SkillReport
	Diffs     []SkillReport
}

// InstallOptions is the inject-the-world struct callers pass to Install.
// The zero value installs the embedded bundle into Target with default
// (no-overwrite) semantics, but Source must always be set explicitly so
// tests cannot accidentally reach into the real binary.
type InstallOptions struct {
	// Target is the destination root that will end up containing
	// `<Target>/marunage-triage/SKILL.md` etc. Created with 0700 if
	// absent.
	Target string
	// Source is the read-only Skills layout. Must contain top-level
	// directories of the shape `marunage-*/SKILL.md`.
	Source fs.FS

	// Force overwrites existing on-disk SKILL.md files even if their
	// content differs from Source.
	Force bool
	// Diff prints a unified-style diff to Out instead of writing.
	Diff bool
	// CheckUpdates lists embedded vs on-disk versions to Out, no writes.
	CheckUpdates bool
	// Merge prompts the user once per conflicting skill on In and acts
	// on the answer (`o` overwrite, `s` skip, `d` show diff and re-ask).
	// Merge implies neither Force nor Diff; it is the third path.
	Merge bool

	// Out is the writer Diff, CheckUpdates and Merge render to. nil
	// falls back to os.Stdout so the CLI surface does not need to plumb
	// stdout for happy-path callers.
	Out io.Writer
	// In is the reader Merge consumes prompt answers from. nil falls
	// back to os.Stdin. Tests pass a strings.Reader so they do not
	// need an interactive terminal.
	In io.Reader
}

// Install applies opts and returns the per-skill outcomes.
//
// The function is the package's single public entry point so the CLI
// surface (`internal/cli/setup.go`) does not need to know about the
// embedded FS, the parser, or the per-mode write semantics.
func Install(opts InstallOptions) (InstallResult, error) {
	if opts.Source == nil {
		return InstallResult{}, fmt.Errorf("skills.Install: Source must not be nil")
	}
	if opts.Target == "" {
		return InstallResult{}, fmt.Errorf("skills.Install: Target must not be empty")
	}

	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}

	// Discover the skills the source carries. Sorting the list keeps the
	// human-visible report deterministic across runs.
	skillNames, err := listSkillDirs(opts.Source)
	if err != nil {
		return InstallResult{}, err
	}

	if !opts.Diff && !opts.CheckUpdates {
		if err := os.MkdirAll(opts.Target, 0o700); err != nil {
			return InstallResult{}, fmt.Errorf("mkdir %s: %w", opts.Target, err)
		}
	}

	res := InstallResult{}
	for _, name := range skillNames {
		srcBody, err := fs.ReadFile(opts.Source, filepath.ToSlash(filepath.Join(name, "SKILL.md")))
		if err != nil {
			return res, fmt.Errorf("read source %s/SKILL.md: %w", name, err)
		}
		newVersion, vErr := ExtractVersion(srcBody)
		if vErr != nil {
			return res, fmt.Errorf("source %s: %w", name, vErr)
		}

		dstPath := filepath.Join(opts.Target, name, "SKILL.md")
		existing, hasExisting := readIfExists(dstPath)
		oldVersion := ""
		if hasExisting {
			if v, err := ExtractVersion(existing); err == nil {
				oldVersion = v
			}
		}
		report := SkillReport{Name: name, OldVersion: oldVersion, NewVersion: newVersion}

		switch {
		case opts.CheckUpdates:
			fmt.Fprintf(out, "%s: on-disk=%s embedded=%s\n",
				name, displayVersion(oldVersion, hasExisting), newVersion)
			if hasExisting && !bytes.Equal(existing, srcBody) {
				res.Diffs = append(res.Diffs, report)
			}
		case opts.Diff:
			if !hasExisting {
				fmt.Fprintf(out, "%s: not installed (would install version %s)\n", name, newVersion)
				res.Diffs = append(res.Diffs, report)
				continue
			}
			if bytes.Equal(existing, srcBody) {
				fmt.Fprintf(out, "%s: identical (version %s)\n", name, newVersion)
				continue
			}
			writeUnifiedDiff(out, name, existing, srcBody)
			res.Diffs = append(res.Diffs, report)
		case !hasExisting:
			if err := writeSkill(dstPath, srcBody); err != nil {
				return res, err
			}
			res.Installed = append(res.Installed, report)
		case bytes.Equal(existing, srcBody):
			res.Skipped = append(res.Skipped, report)
		case opts.Merge:
			overwrite, err := promptMerge(out, in, name, existing, srcBody)
			if err != nil {
				return res, err
			}
			if !overwrite {
				res.Skipped = append(res.Skipped, report)
				continue
			}
			if err := writeSkill(dstPath, srcBody); err != nil {
				return res, err
			}
			res.Updated = append(res.Updated, report)
		case !opts.Force:
			res.Skipped = append(res.Skipped, report)
		default:
			if err := writeSkill(dstPath, srcBody); err != nil {
				return res, err
			}
			res.Updated = append(res.Updated, report)
		}
	}

	if !opts.Diff && !opts.CheckUpdates {
		if err := validateInstalledTriage(opts.Target); err != nil {
			return res, err
		}
	}

	return res, nil
}

// listSkillDirs returns every top-level entry under root that looks like
// a `marunage-<name>` skill directory. The "marunage-" prefix is the
// convention `~/.claude/skills/marunage-*` documents; entries that do
// not match it are ignored so a user-supplied --from-dir containing
// unrelated subdirs (e.g. `.git`) is not mishandled.
func listSkillDirs(root fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return nil, fmt.Errorf("read source root: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "marunage-") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// readIfExists returns (body, true) when path is a regular file we
// could read; (nil, false) when it does not exist; and the read error
// for anything else (permission denied etc.) — surfaced via the second
// bool being false plus the body being nil keeps the call sites short
// at the cost of conflating "missing" with "unreadable", which is
// acceptable here because the next write attempt would surface the
// permission error immediately.
func readIfExists(path string) ([]byte, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return body, true
}

// writeSkill writes body to path with 0600, materialising the parent
// directory at 0700 if needed, and uses the tmp+rename pattern so a
// crash mid-write never leaves a half-written SKILL.md behind.
func writeSkill(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".skill-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// validateInstalledTriage runs the required-section check on the
// post-install on-disk triage SKILL.md. We intentionally validate the
// installed file rather than the source body so a user-edited copy that
// failed validation cannot be left in place by an `Install` call that
// reported success.
func validateInstalledTriage(target string) error {
	path := filepath.Join(target, triageSkillName, "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		// Triage is optional in --from-dir scenarios that ship only
		// other skills. If the file does not exist after install,
		// there's nothing to validate.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read installed triage: %w", err)
	}
	return ValidateRequiredSections(body, RequiredTriageSections)
}

// displayVersion turns the (oldVersion, hasExisting) pair into the
// printable token --check-updates uses. Hand-edited files with no
// metadata read as `unknown` so the operator notices.
func displayVersion(v string, hasExisting bool) string {
	switch {
	case !hasExisting:
		return "(absent)"
	case v == "":
		return "unknown"
	default:
		return v
	}
}

// promptMerge runs the simple three-choice prompt --merge offers per
// conflicting skill: overwrite (`o`), skip (`s`), or show diff and
// re-prompt (`d`). Returning (true, nil) means "overwrite"; (false,
// nil) means "skip". An EOF on the input stream defaults to skip so a
// closed pipe never causes data loss.
func promptMerge(out io.Writer, in io.Reader, name string, oldBody, newBody []byte) (bool, error) {
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprintf(out, "%s differs from the embedded copy. (o)verwrite / (s)kip / (d)iff [s]: ", name)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return false, fmt.Errorf("read merge prompt: %w", err)
			}
			// Closed input — default to the safe action so a CI
			// run that accidentally enables --merge cannot lose
			// the user's edits.
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "o", "overwrite":
			return true, nil
		case "s", "skip", "":
			return false, nil
		case "d", "diff":
			writeUnifiedDiff(out, name, oldBody, newBody)
		default:
			fmt.Fprintf(out, "unknown choice; pick o/s/d\n")
		}
	}
}

// writeUnifiedDiff renders a minimal unified-style diff for the two
// bodies. We do not try to mirror GNU diff exactly: callers want a
// human-readable change marker and a quick visual cue, not a patch
// they can pipe back into `patch -p1`.
func writeUnifiedDiff(w io.Writer, name string, oldBody, newBody []byte) {
	fmt.Fprintf(w, "--- %s (on-disk)\n", name)
	fmt.Fprintf(w, "+++ %s (embedded)\n", name)
	oldLines := strings.Split(string(oldBody), "\n")
	newLines := strings.Split(string(newBody), "\n")
	for _, l := range oldLines {
		fmt.Fprintf(w, "- %s\n", l)
	}
	for _, l := range newLines {
		fmt.Fprintf(w, "+ %s\n", l)
	}
}

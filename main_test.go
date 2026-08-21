package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lockyc/docgraph/v3/internal/audit"
)

// noCfg returns a leaks-config path guaranteed not to exist, so a test that isn't
// about leak rules stays deterministic (no rules → nothing scanned) instead of
// picking up the dev machine's real ~/.config/docgraph/leaks.toml.
func noCfg(dir string) string { return filepath.Join(dir, "no-leaks-cfg") }

// The default config home is XDG (~/.config), not os.UserConfigDir() — which on
// macOS is ~/Library/Application Support, the wrong GUI-app home for a CLI tool.
func TestResolveLeaksConfigXDG(t *testing.T) {
	if got, _ := resolveLeaksConfig("/explicit/x.toml"); got != "/explicit/x.toml" {
		t.Errorf("--leaks-config should win, got %q", got)
	}
	t.Setenv("DOCGRAPH_LEAKS", "/env/y.toml")
	if got, _ := resolveLeaksConfig(""); got != "/env/y.toml" {
		t.Errorf("$DOCGRAPH_LEAKS should win over XDG, got %q", got)
	}
	t.Setenv("DOCGRAPH_LEAKS", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := mustResolve(t), filepath.Join("/xdg", "docgraph", "leaks.toml"); got != want {
		t.Errorf("XDG default = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got, want := mustResolve(t), filepath.Join(home, ".config", "docgraph", "leaks.toml"); got != want {
		t.Errorf("fallback = %q, want %q (~/.config, not ~/Library/Application Support)", got, want)
	}
}

func mustResolve(t *testing.T) string {
	t.Helper()
	got, err := resolveLeaksConfig("")
	if err != nil {
		t.Fatalf("resolveLeaksConfig: %v", err)
	}
	return got
}

func mkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	write("CLAUDE.md", "[i](docs/index.md)\n")
	write("docs/index.md", "[gone](missing.md)\n")
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init")
	git("add", "CLAUDE.md", "docs/index.md")
	return dir
}

func TestRunFindingsExit1(t *testing.T) {
	dir := mkRepo(t)
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("BROKEN LINKS (1)")) {
		t.Errorf("missing broken-links section:\n%s", out.String())
	}
}

func mkOrphanRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	// CLAUDE.md and docs/orphan.md now each need a frontmatter block (Task 5's
	// mandatory rule), which would otherwise make both metadata islands too — give
	// them a mutual frontmatter `see-also` edge to stay clean on that check. A
	// frontmatter edge doesn't feed content-graph reachability (stripped before link
	// scanning), so docs/orphan.md stays a genuine content-graph orphan below.
	write("CLAUDE.md", "---\ntype: index\nlinks:\n  - rel: see-also\n    to: docs/orphan.md\n---\nhub with no links\n")
	write("docs/orphan.md", "---\ntype: reference\n---\nunreferenced\n")
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init")
	git("add", "CLAUDE.md", "docs/orphan.md")
	return dir
}

func TestRunSkipExcludesOrphans(t *testing.T) {
	dir := mkOrphanRepo(t)
	var out, errb bytes.Buffer
	code := run([]string{"--skip", "orphans", "--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (orphans skipped)\n%s", code, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("ORPHANS")) {
		t.Errorf("ORPHANS section shown despite --skip orphans:\n%s", out.String())
	}
}

func TestRunEnforcesOrphansByDefault(t *testing.T) {
	dir := mkOrphanRepo(t)
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (orphan gated by default)", code)
	}
}

// mkMetadataIslandRepo builds a repo where docs/a.md is reachable in the
// CONTENT graph (linked from the root, so `orphans` stays clean) but carries a
// frontmatter block with no doc->doc `links` edge — a metadata island for the
// `disconnected` check, isolated from the other checks.
func mkMetadataIslandRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	// CLAUDE.md now needs a frontmatter block itself (Task 5's mandatory rule), which
	// would make IT a second metadata island too unless it carries a real doc->doc
	// edge. Point that edge at README.md (frontmatter-exempt by basename, so it
	// doesn't need its own block) rather than at docs/a.md, so the island under test
	// stays isolated to exactly docs/a.md.
	write("README.md", "# repo\n")
	write("CLAUDE.md", "---\ntype: index\nlinks:\n  - rel: see-also\n    to: README.md\n---\nhub\n[a](docs/a.md)\n")
	write("docs/a.md", "---\ntype: reference\n---\nno metadata edges\n")
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init")
	git("add", "CLAUDE.md", "docs/a.md", "README.md")
	return dir
}

func TestRunSkipExcludesDisconnected(t *testing.T) {
	dir := mkMetadataIslandRepo(t)
	var out, errb bytes.Buffer
	code := run([]string{"--skip", "disconnected", "--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (disconnected skipped)\n%s", code, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("DISCONNECTED")) {
		t.Errorf("DISCONNECTED section shown despite --skip disconnected:\n%s", out.String())
	}
}

func TestRunEnforcesDisconnectedByDefault(t *testing.T) {
	dir := mkMetadataIslandRepo(t)
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (metadata island gated by default)\n%s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("DISCONNECTED (1)")) || !bytes.Contains(out.Bytes(), []byte("docs/a.md")) {
		t.Errorf("missing disconnected finding for docs/a.md:\n%s", out.String())
	}
}

func TestPrintReportFrontmatterSection(t *testing.T) {
	var buf bytes.Buffer
	rep := audit.Report{FrontmatterFindings: []audit.FrontmatterFinding{{File: "docs/x.md", Detail: "missing type"}}}
	sel := map[string]bool{"frontmatter": true}
	if !printReport(&buf, rep, nil, sel) {
		t.Fatal("printReport returned false, want true (has a frontmatter finding)")
	}
	if !bytes.Contains(buf.Bytes(), []byte("FRONTMATTER (1)")) || !bytes.Contains(buf.Bytes(), []byte("docs/x.md")) {
		t.Errorf("output missing frontmatter section:\n%s", buf.String())
	}
}

func TestPrintReportDisconnectedSection(t *testing.T) {
	var buf bytes.Buffer
	rep := audit.Report{Disconnected: []string{"docs/only-covers.md"}}
	sel := map[string]bool{"disconnected": true}
	if !printReport(&buf, rep, nil, sel) {
		t.Fatal("printReport returned false, want true (a metadata island is a finding)")
	}
	if !bytes.Contains(buf.Bytes(), []byte("DISCONNECTED (1)")) || !bytes.Contains(buf.Bytes(), []byte("docs/only-covers.md")) {
		t.Errorf("output missing disconnected section:\n%s", buf.String())
	}
}

func TestPrintReportEdgesSection(t *testing.T) {
	var buf bytes.Buffer
	rep := audit.Report{BrokenEdges: []audit.BrokenEdge{{Source: "docs/a.md", Rel: "covers", Target: "scripts/x.sh", Reason: "target does not exist"}}}
	sel := map[string]bool{"edges": true}
	if !printReport(&buf, rep, nil, sel) {
		t.Fatal("printReport returned false, want true")
	}
	if !bytes.Contains(buf.Bytes(), []byte("EDGES (1)")) || !bytes.Contains(buf.Bytes(), []byte("scripts/x.sh")) {
		t.Errorf("output missing edges section:\n%s", buf.String())
	}
}

func TestRunSkipInvalidExit2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--skip", "bogus", t.TempDir()}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 for invalid check name", code)
	}
}

// The removed --checks flag must fail loudly with a migration message rather than
// flag's cryptic "flag provided but not defined", because old hooks bake in --checks.
func TestRunChecksFlagRemovedExit2(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--checks", "orphans", t.TempDir()}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 for removed --checks flag", code)
	}
	if !strings.Contains(errb.String(), "--checks was removed") || !strings.Contains(errb.String(), "--skip") {
		t.Errorf("want a --checks migration message pointing at --skip, got: %s", errb.String())
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func TestInstallHookDefaultEnforcesAll(t *testing.T) {
	dir := gitInit(t)
	var out, errb bytes.Buffer
	if code := runInstallHook([]string{dir}, &out, &errb); code != 0 {
		t.Fatalf("exit=%d\n%s", code, errb.String())
	}
	hook := filepath.Join(dir, ".githooks", "pre-push")
	b, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	// Default hook runs a bare `docgraph .` — no check selection — so a
	// newly-added check is enforced automatically without regenerating the hook.
	// No longer `exec`'d: a later line (footgun-drift) must run after it.
	if !strings.Contains(string(b), `"$bin" .`) {
		t.Errorf("default hook should run bare `docgraph .`:\n%s", b)
	}
	if strings.Contains(string(b), "--checks") || strings.Contains(string(b), "--skip") {
		t.Errorf("default hook should carry no check flags:\n%s", b)
	}
	// The hook must resolve docgraph even under a minimal PATH (git runs hooks
	// with the caller's PATH; GUI clients / agent harnesses often lack ~/go/bin).
	// Guard the Go-bin fallback so it can't regress to `command -v` only.
	if !strings.Contains(string(b), "$HOME/go/bin") || !strings.Contains(string(b), "docgraph_bin") {
		t.Errorf("hook lost its minimal-PATH fallback (would fail-closed when docgraph isn't on PATH):\n%s", b)
	}
	if fi, _ := os.Stat(hook); fi.Mode()&0o100 == 0 {
		t.Error("hook not executable")
	}
	hp, _ := exec.Command("git", "-C", dir, "config", "core.hooksPath").Output()
	if strings.TrimSpace(string(hp)) != ".githooks" {
		t.Errorf("core.hooksPath = %q, want .githooks", strings.TrimSpace(string(hp)))
	}
}

func TestInstallHookSkip(t *testing.T) {
	dir := gitInit(t)
	var out, errb bytes.Buffer
	if code := runInstallHook([]string{"--skip", "orphans", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit=%d\n%s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, ".githooks", "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `--skip orphans`) {
		t.Errorf("hook missing --skip orphans:\n%s", b)
	}
}

func TestInstallHookRefusesExisting(t *testing.T) {
	dir := gitInit(t)
	os.MkdirAll(filepath.Join(dir, ".githooks"), 0o755)
	os.WriteFile(filepath.Join(dir, ".githooks", "pre-push"), []byte("#existing\n"), 0o755)
	var out, errb bytes.Buffer
	if code := runInstallHook([]string{dir}, &out, &errb); code != 2 {
		t.Fatalf("exit=%d, want 2 (refuse existing without --force)", code)
	}
}

func TestRunNotAGitRepoExit2(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{t.TempDir()}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

// Absent leak config is NON-fatal: leaks runs by default (incl. CI, which has no
// machine-local file), so it warns and scans nothing rather than exit 2.
func TestRunLeaksAbsentConfigNonFatal(t *testing.T) {
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	write("README.md", "nothing sensitive here\n")
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (absent config is non-fatal, no findings)\n%s", code, out.String())
	}
	if !strings.Contains(errb.String(), "no leak rules file") || !strings.Contains(errb.String(), "nothing is scanned") {
		t.Errorf("want a warning that the absent config means nothing is scanned, got: %s", errb.String())
	}
}

// The config is the sole source of rules: with no config, a secret-shaped string
// is NOT flagged — there are no hidden built-in patterns to fall back on.
func TestRunLeaksNoRulesWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	write("README.md", "hello\n")
	write("secrets.env", "AWS=AKIAIOSFODNN7EXAMPLE\n")
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "README.md", "secrets.env").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (no config → no rules → nothing flagged)\n%s", code, out.String())
	}
	// A clean run prints only the terse one-liner (no per-check sections), so the
	// proof of "not flagged" is that the secret value never appears in the output.
	if bytes.Contains(out.Bytes(), []byte("AKIAIOSFODNN7EXAMPLE")) {
		t.Errorf("a secret-shaped string must NOT be flagged without a configured rule:\n%s", out.String())
	}
}

func TestInstallHookIgnorePassthrough(t *testing.T) {
	dir := gitInit(t)
	var out, errb bytes.Buffer
	code := runInstallHook([]string{"--ignore", "**/*_test.go", "--force", dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, ".githooks", "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--ignore '**/*_test.go'") {
		t.Errorf("hook missing --ignore passthrough:\n%s", b)
	}
}

func TestHookScriptRunsBothChecks(t *testing.T) {
	s := hookScript("", nil, false, false)
	if !strings.Contains(s, `"$bin" `) || !strings.Contains(s, ".") {
		t.Fatal("hook must run the whole-state check")
	}
	if !strings.Contains(s, "footgun-drift") {
		t.Fatal("hook must run footgun-drift")
	}
	if !strings.Contains(s, `refs="$(cat)"`) {
		t.Fatal("hook must capture pre-push stdin to feed footgun-drift")
	}
	// footgun-drift is advisory: its hook line must never abort the push, even on
	// an operational error, so it is guarded with `|| true`.
	if !strings.Contains(s, `footgun-drift . || true`) {
		t.Fatal("footgun-drift hook line must be advisory (|| true), never blocking")
	}
}

func TestHookScriptNoFootgunDrift(t *testing.T) {
	s := hookScript("", nil, true, false)
	if strings.Contains(s, "footgun-drift") {
		t.Fatal("--no-footgun-drift must omit the footgun line")
	}
}

func TestRunLeaksBadRegexExit2(t *testing.T) {
	dir := gitInit(t)
	cfg := filepath.Join(dir, "leaks.toml")
	os.WriteFile(cfg, []byte("regex = ['(unclosed']\n"), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", cfg, dir}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (bad regex in config)\n%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "nothing is scanned") {
		t.Errorf("a bad-regex config must fail, not degrade to the absent-config path: %s", errb.String())
	}
	if !strings.Contains(errb.String(), "leaks regex") {
		t.Errorf("want the regex error surfaced, got: %s", errb.String())
	}
}

func TestRunLeaksMalformedTomlExit2(t *testing.T) {
	dir := gitInit(t)
	cfg := filepath.Join(dir, "leaks.toml")
	os.WriteFile(cfg, []byte("this is not = valid = toml\n"), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", cfg, dir}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (malformed TOML)\n%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "nothing is scanned") {
		t.Errorf("a malformed config must fail, not degrade to the absent-config path: %s", errb.String())
	}
}

// An unknown TOML key (e.g. the singular "ignore_group" typo for
// "ignore_groups") decodes with no error and a zero-value field, so a
// [[dir]] silently gets the DefaultGroup default instead of the named group
// the owner meant to suppress — inverting which deny class is live. The
// decode must reject it instead of accepting it quietly.
func TestRunLeaksUnknownKeyExit2(t *testing.T) {
	dir := gitInit(t)
	cfg := filepath.Join(dir, "leaks.toml")
	toml := `terms = ["needle"]

[[group]]
name = "footprint"
terms = ["local-thing"]

[[dir]]
path = "` + dir + `"
ignore = ["**"]
ignore_group = ["footprint"]
`
	os.WriteFile(cfg, []byte(toml), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", cfg, dir}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (unknown key ignore_group)\n%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "nothing is scanned") {
		t.Errorf("an unknown-key config must fail, not degrade to the absent-config path: %s", errb.String())
	}
	if !strings.Contains(errb.String(), "ignore_group") {
		t.Errorf("want the offending key named in the error, got: %s", errb.String())
	}
}

// leaks is enforced by DEFAULT — no opt-in flag needed for a user-configured
// pattern to gate.
func TestLeaksRulesAbsentConfigIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := runLeaksRules([]string{"--leaks-config", noCfg(dir)}, &out, &errb)
	if code != 0 {
		t.Fatalf("absent config should exit 0, got %d (stderr: %s)", code, errb.String())
	}
	if out.String() != "" {
		t.Errorf("absent config should emit no rules, got %q", out.String())
	}
	if !strings.Contains(errb.String(), "nothing to export") {
		t.Errorf("expected absent-config warning, got %q", errb.String())
	}
}

func TestLeaksRulesEmitsRulesAndDropWarning(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "leaks.toml")
	if err := os.WriteFile(cfg, []byte(`terms = ["secret.host"]`+"\n"+`allow = ["secret.hostname"]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runLeaksRules([]string{"--leaks-config", cfg}, &out, &errb)
	if code != 0 {
		t.Fatalf("valid config should exit 0, got %d (stderr: %s)", code, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != `regex:(?i)secret\.host` {
		t.Errorf("stdout = %q, want the escaped term rule", got)
	}
	if !strings.Contains(errb.String(), "ignores 1 allow/allow_regex") {
		t.Errorf("expected drop warning naming the allow count, got %q", errb.String())
	}
}

func TestLeaksRulesMalformedConfigIsFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "leaks.toml")
	if err := os.WriteFile(cfg, []byte(`regex = ["(["]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runLeaksRules([]string{"--leaks-config", cfg}, &out, &errb)
	if code != 2 {
		t.Fatalf("bad regex should exit 2, got %d", code)
	}
}

func TestLeaksRulesRejectsChecksFlag(t *testing.T) {
	var out, errb bytes.Buffer
	code := runLeaksRules([]string{"--checks", "leaks"}, &out, &errb)
	if code != 2 {
		t.Fatalf("--checks should be rejected with exit 2, got %d", code)
	}
	if !strings.Contains(errb.String(), "--checks was removed") {
		t.Errorf("expected migration message, got %q", errb.String())
	}
}

func TestRunEnforcesLeaksByDefault(t *testing.T) {
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	write("README.md", "reach us at admin@example.com today\n")
	cfg := filepath.Join(dir, "leaks.toml")
	os.WriteFile(cfg, []byte("terms = [\"example.com\"]\n"), 0o644)
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	var out, errb bytes.Buffer
	code := run([]string{"--leaks-config", cfg, dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (leak present, enforced by default)\n%s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("LEAKS (1)")) {
		t.Errorf("missing LEAKS section:\n%s", out.String())
	}
}

// TestMain isolates every test in this package from the dev machine's real
// ~/.config and ~/.local/state, so a test can never read the owner's config.toml
// or append junk to the real usage.jsonl if logging is enabled on this machine.
func TestMain(m *testing.M) {
	cfg, _ := os.MkdirTemp("", "docgraph-cfg")
	state, _ := os.MkdirTemp("", "docgraph-state")
	os.Setenv("XDG_CONFIG_HOME", cfg)
	os.Setenv("XDG_STATE_HOME", state)
	os.Unsetenv("DOCGRAPH_CONFIG")
	os.Unsetenv("DOCGRAPH_LOG")
	os.Unsetenv("DOCGRAPH_NO_LOG")
	os.Unsetenv("DOCGRAPH_LEAKS")
	code := m.Run()
	os.RemoveAll(cfg)
	os.RemoveAll(state)
	os.Exit(code)
}

func TestResolveConfigXDG(t *testing.T) {
	if got, _ := resolveConfig("/explicit/c.toml"); got != "/explicit/c.toml" {
		t.Errorf("--config should win, got %q", got)
	}
	t.Setenv("DOCGRAPH_CONFIG", "/env/c.toml")
	if got, _ := resolveConfig(""); got != "/env/c.toml" {
		t.Errorf("$DOCGRAPH_CONFIG should win over XDG, got %q", got)
	}
	t.Setenv("DOCGRAPH_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got, err := resolveConfig("")
	if err != nil || got != filepath.Join("/xdg", "docgraph", "config.toml") {
		t.Errorf("XDG default = %q (%v), want /xdg/docgraph/config.toml", got, err)
	}
}

// A repo with a finding + logging enabled writes exactly one JSONL record.
func TestRunLogsWhenEnabled(t *testing.T) {
	dir := mkRepo(t) // broken link → exit 1
	logf := filepath.Join(t.TempDir(), "usage.jsonl")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("[log]\nenabled = true\nlevel = 1\npath = "+strconv.Quote(logf)+"\n"), 0o644)

	var out, errb bytes.Buffer
	code := run([]string{"--config", cfg, "--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out.String())
	}
	b, err := os.ReadFile(logf)
	if err != nil {
		t.Fatalf("log not written: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 log line, got %d: %q", len(lines), b)
	}
	if !strings.Contains(lines[0], `"cmd":"run"`) || !strings.Contains(lines[0], `"exit":1`) ||
		!strings.Contains(lines[0], `"broken":1`) {
		t.Errorf("record missing expected fields: %s", lines[0])
	}
}

// No config → logging silently off, nothing written (default state on CI/clones).
func TestRunNoLogWhenConfigAbsent(t *testing.T) {
	dir := mkRepo(t)
	logf := filepath.Join(t.TempDir(), "usage.jsonl")
	t.Setenv("DOCGRAPH_LOG", logf)
	var out, errb bytes.Buffer
	run([]string{"--config", noCfg(dir), "--leaks-config", noCfg(dir), dir}, &out, &errb)
	if _, err := os.Stat(logf); !os.IsNotExist(err) {
		t.Errorf("no config should mean no log file, but it exists (err=%v)", err)
	}
}

// A malformed config.toml is NON-fatal for logging: it warns, disables logging, and
// the run still returns its normal exit code — a log-config typo must not block a
// push. (This is the deliberate divergence from leaks, where malformed is fatal.)
func TestRunMalformedConfigNonFatal(t *testing.T) {
	dir := mkRepo(t) // exit 1 on its own
	logf := filepath.Join(t.TempDir(), "usage.jsonl")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("[log]\nenabled = this is not valid toml\n"), 0o644)
	t.Setenv("DOCGRAPH_LOG", logf)

	var out, errb bytes.Buffer
	code := run([]string{"--config", cfg, "--leaks-config", noCfg(dir), dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (malformed log config must NOT change the exit code)\n%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "config") {
		t.Errorf("want a warning mentioning the config problem, got: %s", errb.String())
	}
	if _, err := os.Stat(logf); !os.IsNotExist(err) {
		t.Errorf("malformed config should disable logging (no file), but it exists")
	}
}

// DOCGRAPH_NO_LOG=1 disables logging even when the config enables it.
func TestRunNoLogEnvDisables(t *testing.T) {
	dir := mkRepo(t)
	logf := filepath.Join(t.TempDir(), "usage.jsonl")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("[log]\nenabled = true\nlevel = 1\npath = "+strconv.Quote(logf)+"\n"), 0o644)
	t.Setenv("DOCGRAPH_NO_LOG", "1")

	var out, errb bytes.Buffer
	run([]string{"--config", cfg, "--leaks-config", noCfg(dir), dir}, &out, &errb)
	if _, err := os.Stat(logf); !os.IsNotExist(err) {
		t.Errorf("DOCGRAPH_NO_LOG=1 should suppress logging, but the file exists")
	}
}

// contains reports whether s is present in sl.
func contains(sl []string, s string) bool {
	for _, v := range sl {
		if v == s {
			return true
		}
	}
	return false
}

// commitRepoMain builds a repo, commits `base` content, then commits `head`
// content, returning (dir, baseSHA, headSHA). Mirrors internal/audit's
// commitRepo, duplicated here because main is a separate package with no
// access to the audit package's unexported test helpers.
func commitRepoMain(t *testing.T, base, head map[string]string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	git := func(a ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
		return string(out)
	}
	git("init")
	for p, c := range base {
		write(p, c)
		git("add", p)
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(git("rev-parse", "HEAD"))
	for p, c := range head {
		write(p, c)
		git("add", p)
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "head")
	headSHA := strings.TrimSpace(git("rev-parse", "HEAD"))
	return dir, baseSHA, headSHA
}

// Advisory, not blocking: an added declaration prints the nag but exits 0, so the
// push is never aborted — the message alone prompts the pusher to double-check.
func TestFootgunDriftSubcommandRangeIsAdvisory(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"CLAUDE.md": "intro\n"},
		map[string]string{"CLAUDE.md": "intro\n\n- **Footgun:** no why.\n"},
	)
	var out, errb bytes.Buffer
	code := runFootgunDrift([]string{"--range", base + ".." + head, dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("footgun-drift is advisory — want exit 0, got %d\n%s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("FOOTGUN")) || !bytes.Contains(out.Bytes(), []byte("no why")) {
		t.Fatalf("want a FOOTGUN finding naming the line, got:\n%s", out.String())
	}
}

// No added declaration → no output at all (nothing to nag about), exit 0.
func TestFootgunDriftSubcommandSilentWhenNoDeclaration(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"CLAUDE.md": "intro\n"},
		map[string]string{"CLAUDE.md": "intro\n\njust some added prose, no declaration.\n"},
	)
	var out, errb bytes.Buffer
	code := runFootgunDrift([]string{"--range", base + ".." + head, dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("want exit 0, got %d\n%s", code, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("FOOTGUN")) {
		t.Fatalf("no declaration added → no FOOTGUN output, got:\n%s", out.String())
	}
}

func TestFootgunDriftOffEnv(t *testing.T) {
	t.Setenv("DOCGRAPH_FOOTGUN_OFF", "1")
	var out, errb bytes.Buffer
	code := runFootgunDrift([]string{"--range", "x..y", "."}, &out, &errb)
	if code != 0 {
		t.Fatalf("DOCGRAPH_FOOTGUN_OFF must short-circuit to 0, got %d", code)
	}
}

func TestFootgunsNotInStateChecks(t *testing.T) {
	if contains(checkNames, "footguns") {
		t.Fatal("footguns must NOT be a whole-state check")
	}
}

func TestDocDriftSubcommandBlocksOnDrift(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"x.go": "type OldWidget struct{}\n", "CLAUDE.md": "We use OldWidget.\n"},
		map[string]string{"x.go": "package x\n"},
	)
	var out, errb bytes.Buffer
	code := runDocDrift([]string{"--range", base + ".." + head, dir}, strings.NewReader(""), &out, &errb)
	if code != 2 {
		t.Fatalf("doc-drift blocks -> want exit 2, got %d\nstderr:\n%s", code, errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("doc-drift")) || !bytes.Contains(errb.Bytes(), []byte("OldWidget")) {
		t.Fatalf("want a doc-drift finding on stderr naming OldWidget, got:\n%s", errb.String())
	}
}

// The drift message names each drifting doc, but a symbol scan cannot see a doc
// that governs the changed code without naming a removed symbol. Pointing at
// `covers` is how the sweep it asks for is actionable — and the only thing that
// advertises the view to an agent that never trips a gate.
func TestDocDriftMessagePointsAtCovers(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"x.go": "type OldWidget struct{}\n", "CLAUDE.md": "We use OldWidget.\n"},
		map[string]string{"x.go": "package x\n"},
	)
	var out, errb bytes.Buffer
	if code := runDocDrift([]string{"--range", base + ".." + head, dir}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !bytes.Contains(errb.Bytes(), []byte("docgraph covers <path>")) {
		t.Errorf("drift message must name `docgraph covers <path>` so the sweep it asks for is actionable, got:\n%s", errb.String())
	}
}

func TestDocDriftSubcommandSilentWhenClean(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"CLAUDE.md": "intro\n"},
		map[string]string{"CLAUDE.md": "intro\n\nmore prose\n"},
	)
	var out, errb bytes.Buffer
	code := runDocDrift([]string{"--range", base + ".." + head, dir}, strings.NewReader(""), &out, &errb)
	if code != 0 || errb.Len() != 0 {
		t.Fatalf("no drift -> want exit 0 and no stderr, got %d\n%s", code, errb.String())
	}
}

func TestDocDriftOffKillSwitch(t *testing.T) {
	t.Setenv("DOC_DRIFT_OFF", "1")
	var out, errb bytes.Buffer
	code := runDocDrift([]string{"--range", "a..b", "/nonexistent"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("DOC_DRIFT_OFF=1 -> want exit 0 before any work, got %d", code)
	}
}

// TestDocDriftUnbornHeadNoOp regression-tests bare (no --range) doc-drift in a
// freshly `git init`'d repo with NO commit yet. Before the fix, bare mode
// resolved the diff base to "HEAD" (docDriftDiffBase's own rev-parse-HEAD
// fallback on failure), then `git diff HEAD` against an unborn HEAD exits 128,
// which runDocDrift surfaced as a real git error — wrongly BLOCKING the Stop
// (exit 2) on every turn during repo bootstrap, before any commit exists to
// diff against. It must instead no-op (exit 0, no stderr).
func TestDocDriftUnbornHeadNoOp(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT committed -- this is the unborn-HEAD case.
	var out, errb bytes.Buffer
	code := runDocDrift([]string{dir}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("unborn HEAD -> want exit 0 (no-op), got %d\nstderr:\n%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("unborn HEAD -> want no stderr, got: %s", errb.String())
	}
}

const coversFM = "---\ntype: reference\nlinks:\n  - rel: covers\n    to: src/auth.go\n---\n\n# Auth\n"

// Advisory: a finding prints the nag but exits 0 — the push is never aborted.
func TestCoversDriftSubcommandRangeIsAdvisory(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"docs/auth.md": coversFM, "src/auth.go": "package auth\n"},
		map[string]string{"src/auth.go": "package auth\n\nfunc Login() {}\n"},
	)
	var out, errb bytes.Buffer
	code := runCoversDrift([]string{"--range", base + ".." + head, dir}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("covers-drift is advisory — want exit 0, got %d\n%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "docs/auth.md") || !strings.Contains(out.String(), "src/auth.go") {
		t.Fatalf("want the doc and the covered path named, got:\n%s", out.String())
	}
}

// The generated hook drives runCoversDrift with NO --range at all — it feeds git's
// pre-push stdin lines instead — so that is the only path the production gate
// actually exercises. Every other covers-drift test above passes --range with an
// empty stdin reader, which leaves rangesFromPrePushStdin (main.go) completely
// uncovered. Drive it directly: the line format is git's pre-push hook protocol
// (`<local ref> <local sha1> <remote ref> <remote sha1>`, one ref update per line),
// which rangesFromPrePushStdin parses at main.go:657. A non-zero remote sha (the
// common case: the branch already exists upstream) maps straight to
// RevRange{Base: remoteSHA, Head: localSHA} with no ClosestBase fallback needed.
func TestCoversDriftSubcommandReadsPrePushStdin(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"docs/auth.md": coversFM, "src/auth.go": "package auth\n"},
		map[string]string{"src/auth.go": "package auth\n\nfunc Login() {}\n"},
	)
	stdin := strings.NewReader(fmt.Sprintf("refs/heads/dev %s refs/heads/dev %s\n", head, base))
	var out, errb bytes.Buffer
	code := runCoversDrift([]string{dir}, stdin, &out, &errb)
	if code != 0 {
		t.Fatalf("covers-drift is advisory — want exit 0, got %d\n%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "docs/auth.md") || !strings.Contains(out.String(), "src/auth.go") {
		t.Fatalf("want the doc and the covered path named, got:\n%s", out.String())
	}
}

// No covers edge -> nothing to nag about -> no output at all.
func TestCoversDriftSubcommandSilentWithNoEdges(t *testing.T) {
	dir, base, head := commitRepoMain(t,
		map[string]string{"docs/auth.md": "---\ntype: reference\n---\n\n# Auth\n", "src/auth.go": "package auth\n"},
		map[string]string{"src/auth.go": "package auth\n\nfunc Login() {}\n"},
	)
	var out, errb bytes.Buffer
	code := runCoversDrift([]string{"--range", base + ".." + head, dir}, strings.NewReader(""), &out, &errb)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("want exit 0 and no output, got %d and:\n%s", code, out.String())
	}
}

func TestCoversDriftOffSwitch(t *testing.T) {
	t.Setenv("DOCGRAPH_COVERS_OFF", "1")
	var out, errb bytes.Buffer
	code := runCoversDrift([]string{"--range", "a..b", "/nonexistent"}, strings.NewReader(""), &out, &errb)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("DOCGRAPH_COVERS_OFF=1 -> want exit 0 before any work, got %d", code)
	}
}

func TestHookScriptInvokesCoversDrift(t *testing.T) {
	s := hookScript("", nil, false, false)
	if !strings.Contains(s, "covers-drift") {
		t.Fatalf("generated hook must invoke covers-drift:\n%s", s)
	}
	// covers-drift is advisory on the same terms as footgun-drift: its hook line
	// must never abort the push. The Go-side exit-0 tests can't catch this — the
	// breakage would be in the generated shell, where `set -euo pipefail` turns an
	// exit-2 tool error into a blocked push the moment `|| true` goes missing.
	if !strings.Contains(s, `covers-drift . || true`) {
		t.Fatalf("covers-drift hook line must be advisory (|| true), never blocking:\n%s", s)
	}
	off := hookScript("", nil, false, true)
	if strings.Contains(off, "covers-drift") {
		t.Fatalf("--no-covers-drift must omit it:\n%s", off)
	}
}

func setupRepoMain(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init")
	git("branch", "-M", "wip") // not an integration-branch candidate -> base resolves to HEAD
	for p := range files {
		git("add", p)
	}
	return dir
}

func TestDocDriftLoopGuardNagsOncePerHead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // isolate the marker store
	dir := setupRepoMain(t, map[string]string{
		"x.go": "type OldWidget struct{}\n", "CLAUDE.md": "We use OldWidget.\n",
	})
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "base")
	full := filepath.Join(dir, "x.go")
	os.WriteFile(full, []byte("package x\n"), 0o644) // uncommitted removal -> drift vs HEAD

	// Bare invocation (no --range) uses the guard; on a trunk repo the base is HEAD.
	first := runDocDrift([]string{dir}, strings.NewReader(""), io.Discard, io.Discard)
	if first != 2 {
		t.Fatalf("first bare run must block -> want exit 2, got %d", first)
	}
	second := runDocDrift([]string{dir}, strings.NewReader(""), io.Discard, io.Discard)
	if second != 0 {
		t.Fatalf("same HEAD already nagged -> want exit 0, got %d", second)
	}
}

// setupDriftRepoMain builds a committed repo whose working tree has removed a
// symbol a doc still names — i.e. a bare doc-drift run finds drift. Returns the
// repo dir and the root as audit.GitRoot resolves it (macOS symlinks /var ->
// /private/var, so the state-file key is derived from the RESOLVED root).
func setupDriftRepoMain(t *testing.T) (dir, root, head string) {
	t.Helper()
	dir = setupRepoMain(t, map[string]string{
		"x.go": "type OldWidget struct{}\n", "CLAUDE.md": "We use OldWidget.\n",
	})
	git := func(a ...string) []byte {
		out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
		return out
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "base")
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644) // uncommitted removal
	root, err := audit.GitRoot(dir)
	if err != nil {
		t.Fatalf("GitRoot: %v", err)
	}
	head = strings.TrimSpace(string(git("rev-parse", "HEAD")))
	return dir, root, head
}

// TestDocDriftAlreadyNaggedSkipsScan pins the loop-guard SHORT-CIRCUIT: once a
// HEAD has been nagged, every remaining path returns 0, so the base resolution
// and the diff must not run at all. Asserted structurally rather than by timing:
// the memoized base is poisoned with an unresolvable ref between the two runs,
// which would make a still-scanning second run exit 2 on the git error.
func TestDocDriftAlreadyNaggedSkipsScan(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, root, head := setupDriftRepoMain(t)

	if code := runDocDrift([]string{dir}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("first bare run must block -> want exit 2, got %d", code)
	}
	writeDocDriftBase(root, head, "definitely-not-a-ref")
	if code := runDocDrift([]string{dir}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("already nagged -> want exit 0 without scanning, got %d", code)
	}
}

// TestDocDriftMemoizesDiffBase pins that a bare run records the resolved base
// keyed by HEAD. ClosestBase costs a merge-base + rev-list per integration-branch
// candidate — ~85% of a warm run, re-derived on every Stop hook without this.
func TestDocDriftMemoizesDiffBase(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, root, head := setupDriftRepoMain(t)

	runDocDrift([]string{dir}, strings.NewReader(""), io.Discard, io.Discard)

	got, ok := readDocDriftBase(root, head)
	if !ok {
		t.Fatalf("bare run did not memoize a diff base for HEAD %s", head)
	}
	if got != "HEAD" { // setupRepoMain checks out `wip`: no integration-branch candidate
		t.Fatalf("memoized base = %q, want %q", got, "HEAD")
	}
}

// TestDocDriftBaseCacheKeyedByHead pins that an entry written for a DIFFERENT
// HEAD is ignored — the cache must re-resolve when HEAD moves, or a branch
// switch would keep diffing against the previous branch's base.
func TestDocDriftBaseCacheKeyedByHead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, root, head := setupDriftRepoMain(t)

	writeDocDriftBase(root, "0000000000000000000000000000000000000000", "stale-base")
	if got, ok := readDocDriftBase(root, head); ok {
		t.Fatalf("entry for another HEAD must not be used, got %q", got)
	}
}

func TestRunSchema(t *testing.T) {
	var buf bytes.Buffer
	if code := runSchema(&buf); code != 0 {
		t.Fatalf("runSchema exit = %d, want 0", code)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if m["title"] != "docgraph document frontmatter" {
		t.Errorf("title = %v", m["title"])
	}
}

// chdir switches the process cwd to dir and returns a func restoring the prior
// cwd. Needed because runCovers resolves the repo from "." (like the other
// subcommand entry points that default path to "."), so exercising it requires
// running with the target repo as cwd.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(old) }
}

func TestRunCovers(t *testing.T) {
	dir := setupRepoMain(t, map[string]string{
		"CLAUDE.md": "[a](docs/a.md)\n",
		"docs/a.md": "---\ntype: reference\nlinks: [{rel: covers, to: src/x.go}]\n---\n",
		"src/x.go":  "package x\n",
	})
	var out, errb bytes.Buffer
	// runCovers resolves the repo from ".", so run it with the repo as cwd.
	restore := chdir(t, dir)
	defer restore()
	if code := runCovers([]string{"src/x.go"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if got := out.String(); got != "docs/a.md\n" {
		t.Errorf("covers output = %q, want docs/a.md", got)
	}
}

// TestRunGraphJSON exercises the graph subcommand end-to-end: exit 0, and
// stdout is valid JSON carrying the versioned schema stamp. graph is a
// read-only view (like covers/index/stale above) — never gates, never
// appears in checkNames or the generated hook; the invariant tests below
// confirm the isolation directly.
func TestRunGraphJSON(t *testing.T) {
	dir := setupRepoMain(t, map[string]string{
		"CLAUDE.md": "[a](docs/a.md)\n",
		"docs/a.md": "---\ntype: reference\n---\n# A\n",
	})
	var out, errb bytes.Buffer
	restore := chdir(t, dir)
	defer restore()
	if code := runGraph(nil, &out, &errb); code != 0 {
		t.Fatalf("markdown mode: exit = %d, stderr=%s", code, errb.String())
	}
	out.Reset()
	if code := runGraph([]string{"--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if sv, ok := v["schemaVersion"].(float64); !ok || sv != audit.GraphSchemaVersion {
		t.Fatalf("schemaVersion = %v, want %d", v["schemaVersion"], audit.GraphSchemaVersion)
	}
}

// graph is a read-only view, not a gated check — it must never appear in
// checkNames (the --skip vocabulary) nor in the generated pre-push hook.
func TestGraphNotAGatedCheck(t *testing.T) {
	if contains(checkNames, "graph") {
		t.Fatal("graph must NOT be a whole-state check (checkNames)")
	}
	s := hookScript("", nil, false, false)
	if strings.Contains(s, " graph") || strings.Contains(s, "\"$bin\" graph") {
		t.Fatalf("generated hook must never invoke graph:\n%s", s)
	}
}

// TestGraphRefOnBareRepo exercises `graph --ref` against a bare clone, where
// there is no working tree for GitRoot's `rev-parse --show-toplevel` to
// resolve — --ref must skip GitRoot entirely and read committed state via
// BuildGraphViewAtRef instead.
func TestGraphRefOnBareRepo(t *testing.T) {
	dir := mkRepo(t)
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init")

	bare := t.TempDir() + "/repo.git"
	if out, err := exec.Command("git", "clone", "--bare", dir, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	restore := chdir(t, bare)
	defer restore()

	var out, errb bytes.Buffer
	code := runGraph([]string{"--json", "--ref", "HEAD"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if sv, ok := v["schemaVersion"].(float64); !ok || sv != audit.GraphSchemaVersion {
		t.Fatalf("schemaVersion = %v, want %d", v["schemaVersion"], audit.GraphSchemaVersion)
	}
	if _, ok := v["nodes"]; !ok {
		t.Fatal("missing nodes")
	}
}

// TestGraphRefBadRefExitsNonZero confirms a nonexistent ref fails loudly
// (non-zero exit, no partial JSON on stdout) rather than silently falling
// back to the working tree.
func TestGraphRefBadRefExitsNonZero(t *testing.T) {
	dir := mkRepo(t)
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init")

	restore := chdir(t, dir)
	defer restore()

	var out, errb bytes.Buffer
	code := runGraph([]string{"--json", "--ref", "does-not-exist"}, &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit for a bad ref; stdout=%s", out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("expected no JSON on stdout for a bad ref, got: %s", out.String())
	}
}

func TestPrintReportEdgesHeaderCountsCycles(t *testing.T) {
	var buf bytes.Buffer
	rep := audit.Report{EdgeCycles: [][]string{{"a.md", "b.md"}}}
	if !printReport(&buf, rep, nil, map[string]bool{"edges": true}) {
		t.Fatal("printReport returned false, want true (a cycle is a finding)")
	}
	if !bytes.Contains(buf.Bytes(), []byte("EDGES (1)")) {
		t.Errorf("cycle-only report should show EDGES (1), got:\n%s", buf.String())
	}
}

// Go's flag package stops parsing at the first non-flag argument, so `docgraph .
// --skip leaks` used to silently drop the skip and run the check anyway. A gate
// flag that quietly does nothing is worse than one that errors.
func TestFlagsParseAfterPositionalPath(t *testing.T) {
	call := func(args ...string) (string, int) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		return out.String() + errb.String(), code
	}
	before, codeBefore := call("--skip", "leaks,orphans,broken,untracked,frontmatter,edges,disconnected", ".")
	after, codeAfter := call(".", "--skip", "leaks,orphans,broken,untracked,frontmatter,edges,disconnected")
	if before != after || codeBefore != codeAfter {
		t.Errorf("flags must parse the same before and after the path\n before (%d): %q\n after  (%d): %q",
			codeBefore, before, codeAfter, after)
	}
	if !strings.Contains(after, "every check skipped") {
		t.Errorf("a --skip after the path must take effect, got %q", after)
	}
}

// A footgun declaration can be a 2000-character CLAUDE.md paragraph. The echo
// exists to say WHICH declaration, not to be read in the terminal, so it is
// capped — untrimmed it buried the file:line payload it is attached to.
func TestFootgunDriftEchoIsTrimmed(t *testing.T) {
	long := "- **Footgun — " + strings.Repeat("x", 2000) + "**"
	var out bytes.Buffer
	printFootgunDrift(&out, []audit.FootgunFinding{{File: "CLAUDE.md", Line: 117, Text: long}})
	got := out.String()
	if !strings.Contains(got, "CLAUDE.md:117") {
		t.Errorf("the file:line payload must survive: %q", got)
	}
	if strings.Contains(got, strings.Repeat("x", footgunEchoRunes+1)) {
		t.Errorf("declaration echo must be capped at %d runes, got %q", footgunEchoRunes, got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a trimmed echo must be marked with an ellipsis: %q", got)
	}
}

// Trimming is rune-safe: it must never split a multi-byte character.
func TestFootgunDriftEchoIsRuneSafe(t *testing.T) {
	var out bytes.Buffer
	printFootgunDrift(&out, []audit.FootgunFinding{{File: "d.md", Line: 1, Text: strings.Repeat("é", 300)}})
	if !utf8.ValidString(out.String()) {
		t.Errorf("trimmed echo must stay valid UTF-8: %q", out.String())
	}
}

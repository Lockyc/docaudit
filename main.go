package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/lockyc/docgraph/v3/internal/audit"
)

type multiFlag []string

func (m *multiFlag) String() string     { return "" }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "install-hook":
			os.Exit(runInstallHook(args[1:], os.Stdout, os.Stderr))
		case "leaks-rules":
			os.Exit(runLeaksRules(args[1:], os.Stdout, os.Stderr))
		case "footgun-drift":
			os.Exit(runFootgunDrift(args[1:], os.Stdout, os.Stderr))
		case "covers-drift":
			os.Exit(runCoversDrift(args[1:], os.Stdin, os.Stdout, os.Stderr))
		case "doc-drift":
			os.Exit(runDocDrift(args[1:], os.Stdin, os.Stdout, os.Stderr))
		case "schema":
			os.Exit(runSchema(os.Stdout))
		case "covers":
			os.Exit(runCovers(args[1:], os.Stdout, os.Stderr))
		case "index":
			os.Exit(runIndex(args[1:], os.Stdout, os.Stderr))
		case "stale":
			os.Exit(runStale(args[1:], os.Stdout, os.Stderr))
		case "graph":
			os.Exit(runGraph(args[1:], os.Stdout, os.Stderr))
		case "version", "--version", "-v":
			fmt.Println("docgraph " + version)
			os.Exit(0)
		}
	}
	os.Exit(run(args, os.Stdout, os.Stderr))
}

// checksFlagRemoved reports (with a migration message) whether args still use the
// removed --checks flag. docgraph enforces every check by default now — an
// allow-list of checks to *run* can't enforce, because a newly-added check is
// silently absent from every existing --checks list. Excluding a check is the
// explicit exception (--skip). Old hooks bake in `--checks …`, so a clear message
// beats flag's cryptic "flag provided but not defined".
func checksFlagRemoved(args []string, stderr io.Writer) bool {
	for _, a := range args {
		if a == "--checks" || a == "-checks" ||
			strings.HasPrefix(a, "--checks=") || strings.HasPrefix(a, "-checks=") {
			fmt.Fprintln(stderr, "docgraph: --checks was removed in v2 — all checks are enforced by default.")
			fmt.Fprintln(stderr, "  exclude one with --skip <check[,check]>, and regenerate any installed hook:")
			fmt.Fprintln(stderr, "  docgraph install-hook --force")
			return true
		}
	}
	return false
}

// runInstallHook writes a tracked .githooks/pre-push that runs docgraph, and
// points core.hooksPath at .githooks (activated for this clone). The hook fails
// closed: if docgraph isn't installed the push is blocked, because a gate that
// silently skips when its tool is missing is a false green, not a gate.
func runInstallHook(args []string, stdout, stderr io.Writer) int {
	if checksFlagRemoved(args, stderr) {
		return 2
	}
	fs := flag.NewFlagSet("docgraph install-hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	skip := fs.String("skip", "", "checks to EXCLUDE from the gate, comma-separated (default: none — all enforced)")
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "path glob to exclude from the gated scan (repeatable)")
	force := fs.Bool("force", false, "overwrite an existing .githooks/pre-push")
	noFootgun := fs.Bool("no-footgun-drift", false, "omit the diff-scoped footgun-drift check from the generated hook")
	noCovers := fs.Bool("no-covers-drift", false, "omit the diff-scoped covers-drift check from the generated hook")
	positional, perr := parseArgs(fs, args)
	if perr != nil {
	}
	if _, err := parseSkip(*skip); err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	path := "."
	if len(positional) > 0 {
		path = positional[0]
	}
	root, err := audit.GitRoot(path)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: not a git repository: %s\n", path)
		return 2
	}
	hookPath := filepath.Join(root, ".githooks", "pre-push")
	if _, err := os.Stat(hookPath); err == nil && !*force {
		fmt.Fprintf(stderr, "docgraph: %s already exists — integrate manually or pass --force\n", filepath.Join(".githooks", "pre-push"))
		return 2
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if err := os.WriteFile(hookPath, []byte(hookScript(*skip, ignores, *noFootgun, *noCovers)), 0o755); err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if err := exec.Command("git", "-C", root, "config", "core.hooksPath", ".githooks").Run(); err != nil {
		fmt.Fprintf(stderr, "docgraph: git config core.hooksPath failed: %v\n", err)
		return 2
	}
	if *skip == "" {
		fmt.Fprintln(stdout, "installed .githooks/pre-push (enforcing all checks); core.hooksPath -> .githooks")
	} else {
		fmt.Fprintf(stdout, "installed .githooks/pre-push (enforcing all checks except %s); core.hooksPath -> .githooks\n", *skip)
	}
	return 0
}

// runLeaksRules exports the global leak config as a git-filter-repo --replace-text
// rules file on stdout (rules only — filter-repo has no comment syntax), with
// warnings + a drop summary on stderr. It is NON-destructive: it reads only the
// TOML, never git history; the actual history rewrite is a separate, external
// `git filter-repo --replace-text` step. Config resolution and the absent/malformed
// exit contract mirror the leaks scan.
func runLeaksRules(args []string, stdout, stderr io.Writer) int {
	if checksFlagRemoved(args, stderr) {
		return 2
	}
	fs := flag.NewFlagSet("docgraph leaks-rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	leaksConfig := fs.String("leaks-config", "", "path to the global leaks.toml (default: $DOCGRAPH_LEAKS or $XDG_CONFIG_HOME/docgraph/leaks.toml, else ~/.config/docgraph/leaks.toml)")
	if _, perr := parseArgs(fs, args); perr != nil {
		return 2
	}
	cfgPath, err := resolveLeaksConfig(*leaksConfig)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	cfg, err := loadLeakConfig(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		// Absent config is NOT fatal (same stance as the scan): the global rules file
		// is the normal machine-local-only artifact, absent in CI / fresh clones.
		fmt.Fprintf(stderr, "docgraph: no leak rules file at %s — nothing to export.\n", cfgPath)
		return 0
	} else if err != nil {
		fmt.Fprintf(stderr, "docgraph: leaks config %s: %v\n", cfgPath, err)
		return 2
	}
	// Malformed regex / non-absolute [[dir]] path aren't caught by TOML decode —
	// validate via the same compile the scan runs. Fatal, like the scan.
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "docgraph: leaks config %s: %v\n", cfgPath, err)
		return 2
	}
	lines, dropped := audit.ReplaceTextRules(cfg)
	for _, l := range lines {
		fmt.Fprintln(stdout, l)
	}
	if dropped.Allows > 0 || dropped.Dirs > 0 {
		fmt.Fprintf(stderr, "docgraph: leaks-rules ignores %d allow/allow_regex and %d [[dir]] rule(s) — filter-repo\n", dropped.Allows, dropped.Dirs)
		fmt.Fprintln(stderr, "  rewrites by content across all paths/history, so exceptions and dir-scoping do not")
		fmt.Fprintln(stderr, "  apply. Review the rewrite result.")
	}
	return 0
}

// docgraphBinFunc returns the docgraph_bin() shell function the generated hook
// uses to resolve docgraph even under a minimal hook PATH. Git runs hooks with
// whatever PATH the caller had; GUI clients and some agent harnesses push with a
// bare PATH that omits ~/go/bin, so 'command -v docgraph' alone is unreliable and
// would make the gate fail-closed (blocked) purely because it couldn't see an
// installed binary. Fall back to the Go install dirs before giving up. Both the
// whole-state check and footgun-drift share this one resolution — do not retype
// it inline a second time.
func docgraphBinFunc() string {
	return `docgraph_bin() {
  if command -v docgraph >/dev/null 2>&1; then command -v docgraph; return; fi
  local d
  for d in "${GOBIN:-}" "${GOPATH:+${GOPATH%%:*}/bin}" "$HOME/go/bin"; do
    [ -n "$d" ] && [ -x "$d/docgraph" ] && { printf '%s\n' "$d/docgraph"; return; }
  done
  if command -v go >/dev/null 2>&1; then
    d="$(go env GOBIN 2>/dev/null)"; [ -z "$d" ] && d="$(go env GOPATH 2>/dev/null)/bin"
    [ -x "$d/docgraph" ] && { printf '%s\n' "$d/docgraph"; return; }
  fi
  return 1
}`
}

// hookScript generates the tracked .githooks/pre-push gate. It runs the
// whole-state check (`docgraph .`) and then the diff-scoped riders — unless
// noFootgun, `docgraph footgun-drift`; unless noCovers, `docgraph covers-drift`
// — each fed git's pre-push stdin (ref lines: local/remote SHA pairs for what's
// being pushed) so they can scope themselves to the pushed commit range. The
// whole-state line is a plain command, not `exec` — `exec` would replace the
// shell process, so a failing `docgraph .` would never reach the rider lines
// below it. Under `set -e` a non-zero exit from the whole-state command aborts
// the script (and the push) immediately, so its fail-closed behavior is
// unchanged. Both rider lines are ADVISORY (`|| true`, and each subcommand exits
// 0 on findings): they print a nag but never block.
func hookScript(skip string, ignores []string, noFootgun, noCovers bool) string {
	args := ""
	if skip != "" {
		args += " --skip " + skip
	}
	for _, g := range ignores {
		args += " --ignore '" + g + "'"
	}
	stateLine := `"$bin"` + args + ` .`
	footgun := ""
	if !noFootgun {
		// Advisory, never blocks: footgun-drift exits 0 on findings, and `|| true`
		// swallows even an operational error so this line can never abort a push.
		footgun = `
# Diff-scoped ADVISORY nag: footgun declarations ADDED in the pushed range. Never blocks.
printf '%s' "$refs" | "$bin" footgun-drift . || true`
	}
	covers := ""
	if !noCovers {
		// Advisory on the same terms as footgun-drift above: covers-drift exits 0 on
		// findings, and `|| true` swallows even an operational error.
		covers = `
# Diff-scoped ADVISORY nag: docs whose 'covers' edge points at code this push changed. Never blocks.
printf '%s' "$refs" | "$bin" covers-drift . || true`
	}
	return `#!/usr/bin/env bash
# docgraph pre-push gate — installed by 'docgraph install-hook'. Activated per
# clone via core.hooksPath -> .githooks. Fails closed: if docgraph can't be found
# the push is blocked (install: go install github.com/lockyc/docgraph/v3@latest).
set -euo pipefail
refs="$(cat)"   # git feeds pre-push ref lines on stdin; captured before running anything

` + docgraphBinFunc() + `

if ! bin="$(docgraph_bin)"; then
  echo "docgraph: not found on PATH or in the Go bin dir — push blocked (fail-closed)." >&2
  echo "  install it: go install github.com/lockyc/docgraph/v3@latest" >&2
  exit 1
fi
` + stateLine + footgun + covers + `
`
}

var checkNames = []string{"orphans", "broken", "untracked", "leaks", "frontmatter", "edges", "disconnected"}

func run(args []string, stdout, stderr io.Writer) int {
	if checksFlagRemoved(args, stderr) {
		return 2
	}
	fs := flag.NewFlagSet("docgraph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var roots, ignores multiFlag
	fs.Var(&roots, "root", "extra root doc to start reachability from (repeatable)")
	fs.Var(&ignores, "ignore", "glob to exclude from checks (repeatable)")
	skip := fs.String("skip", "", "checks to EXCLUDE, comma-separated (default: none — all enforced: orphans,broken,untracked,leaks,frontmatter,edges,disconnected)")
	leaksConfig := fs.String("leaks-config", "", "path to the global leaks.toml (default: $DOCGRAPH_LEAKS or $XDG_CONFIG_HOME/docgraph/leaks.toml, else ~/.config/docgraph/leaks.toml)")
	config := fs.String("config", "", "path to the global config.toml, holding [log] (default: $DOCGRAPH_CONFIG or $XDG_CONFIG_HOME/docgraph/config.toml)")
	positional, perr := parseArgs(fs, args)
	if perr != nil {
	}
	selected, err := parseSkip(*skip)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if len(selected) == 0 {
		fmt.Fprintln(stderr, "docgraph: every check skipped — nothing is being enforced")
	}
	path := "."
	if len(positional) > 0 {
		path = positional[0]
	}
	root, err := audit.GitRoot(path)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: not a git repository: %s\n", path)
		return 2
	}
	rep, err := audit.Audit(root, audit.Options{ExtraRoots: roots, Ignores: ignores})
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	var leaks []audit.LeakFinding
	if selected["leaks"] {
		cfgPath, err := resolveLeaksConfig(*leaksConfig)
		if err != nil {
			fmt.Fprintf(stderr, "docgraph: %v\n", err)
			return 2
		}
		cfg, err := loadLeakConfig(cfgPath)
		if errors.Is(err, os.ErrNotExist) {
			// Absent config is NOT fatal: leaks runs by default (incl. CI, which has
			// no machine-local config), so a hard-fail would brick every push there.
			// The config is the sole source of rules — with none, the scan is a no-op,
			// and the warning nudges the owner to define their footprint file.
			fmt.Fprintf(stderr, "docgraph: no leak rules file at %s — the leaks check has no rules, so nothing is scanned;\n", cfgPath)
			fmt.Fprintln(stderr, "  add one (or pass --leaks-config) to define your leak patterns.")
			cfg = audit.LeakConfig{}
		} else if err != nil {
			// Present-but-malformed TOML IS fatal: a real config bug, not "not set up yet".
			fmt.Fprintf(stderr, "docgraph: leaks config %s: %v\n", cfgPath, err)
			return 2
		}
		leaks, err = audit.LeakScan(root, cfg, ignores)
		if err != nil {
			// A bad regexp in an otherwise-valid config surfaces here — also fatal.
			fmt.Fprintf(stderr, "docgraph: leaks config %s: %v\n", cfgPath, err)
			return 2
		}
	}
	findings := printReport(stdout, rep, leaks, selected)
	exit := 0
	if findings {
		exit = 1
	}
	maybeLog(*config, "run", root, exit, rep, leaks, selected, stderr)
	return exit
}

// maybeLog appends one usage record for this run when logging is opted in. It is
// best-effort and side-channel: it never changes the exit code and never returns an
// error. DOCGRAPH_NO_LOG short-circuits it. A malformed config.toml is warned about
// and disables logging — NOT fatal, unlike a malformed leaks.toml: logging is
// auxiliary, so a log-config typo must not block a push.
func maybeLog(configFlag, cmd, root string, exit int, rep audit.Report, leaks []audit.LeakFinding, sel map[string]bool, stderr io.Writer) {
	if os.Getenv("DOCGRAPH_NO_LOG") != "" {
		return
	}
	cfgPath, err := resolveConfig(configFlag)
	if err != nil {
		return
	}
	logCfg, err := loadLogConfig(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return // absent config → logging silently off (the normal CI/clone state).
	} else if err != nil {
		fmt.Fprintf(stderr, "docgraph: config %s: %v — logging disabled (non-fatal)\n", cfgPath, err)
		return
	}
	if !logCfg.Active() {
		return
	}
	logPath, err := audit.LogPath(logCfg.Path)
	if err != nil {
		return
	}
	rec := audit.BuildRecord(cmd, root, version, exit, rep, leaks, sel, logCfg.Level, time.Now())
	_ = audit.LogRun(logPath, rec) // best-effort: a gate never fails because the log is unwritable.
}

// resolveConfig resolves the global config.toml: --config > $DOCGRAPH_CONFIG >
// $XDG_CONFIG_HOME/docgraph/config.toml (else ~/.config/...). Same XDG discipline
// as resolveLeaksConfig — never os.UserConfigDir() (wrong on macOS for a CLI tool).
func resolveConfig(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if env := os.Getenv("DOCGRAPH_CONFIG"); env != "" {
		return env, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "docgraph", "config.toml"), nil
}

// loadLogConfig decodes the [log] table of config.toml. An absent file returns
// os.ErrNotExist (the caller treats it as silently-off); a malformed file returns a
// decode error the caller warns on without failing the run.
func loadLogConfig(path string) (audit.LogConfig, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return audit.LogConfig{}, err
	}
	var fc struct {
		Log audit.LogConfig `toml:"log"`
	}
	_, err := toml.DecodeFile(path, &fc)
	return fc.Log, err
}

// parseSkip returns the set of checks to RUN: every check by default, minus the
// comma-separated names in s. An unknown name is an error. Enforcement is the
// default; skipping is the explicit, per-repo exception (e.g. a nav-driven MkDocs
// repo skips orphans). A newly-added check is enforced everywhere automatically —
// nobody has to remember to add it to a run-list.
// parseArgs parses flags that appear ANYWHERE in args, returning the positional
// arguments. Go's flag package stops at the first non-flag argument, so a plain
// fs.Parse turns `docgraph . --skip leaks` into path="." plus two ignored
// strings: the skip is silently dropped and the check it named runs anyway. A
// gate flag that quietly does nothing is the exact failure this tool exists to
// prevent, and it reads as "docgraph ignored me" rather than as a usage error.
// Re-parsing what follows each positional lets flag itself decide which tokens
// are flag values, so `--ignore x` is never mistaken for a positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

func parseSkip(s string) (map[string]bool, error) {
	sel := map[string]bool{}
	for _, name := range checkNames {
		sel[name] = true
	}
	for _, c := range strings.Split(s, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		valid := false
		for _, name := range checkNames {
			if c == name {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("unknown check %q (valid: %s)", c, strings.Join(checkNames, ","))
		}
		delete(sel, c)
	}
	return sel, nil
}

func resolveLeaksConfig(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if env := os.Getenv("DOCGRAPH_LEAKS"); env != "" {
		return env, nil
	}
	// XDG, not os.UserConfigDir(): docgraph is a CLI tool, and os.UserConfigDir()
	// returns ~/Library/Application Support on macOS (Apple's GUI-app convention),
	// which is the wrong home for a dev tool. Honor $XDG_CONFIG_HOME, else ~/.config.
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "docgraph", "leaks.toml"), nil
}

// loadLeakConfig decodes the global leaks.toml, rejecting any key the schema
// doesn't recognize. toml.DecodeFile silently ignores unknown keys by
// default, which is exactly the failure mode groups introduced: a typo like
// "ignore_group" for "ignore_groups" decodes with err == nil and a
// zero-value field, so a [[dir]] silently falls back to the DefaultGroup
// default instead of the named group the owner meant — inverting which deny
// class a blanket ignore leaves live. Checking MetaData.Undecoded() turns
// that into the same fatal-config path a malformed TOML file already takes.
func loadLeakConfig(path string) (audit.LeakConfig, error) {
	var cfg audit.LeakConfig
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return cfg, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	return cfg, nil
}

// printReport prints the outcome of the checks being run and reports whether any
// has findings. A clean run is a single terse line, so a green pre-push gate does
// not bury the terminal in headers nobody reads. Only on findings does the output
// turn self-describing — a reader (often a fresh agent seeing only a failed `git
// push`) should then learn from the text alone what docgraph is, what a finding
// means, why a non-zero exit aborts a push, and how to remediate: banner, the
// sections that actually have findings, and the explain-and-remediate footer.
func printReport(w io.Writer, r audit.Report, leaks []audit.LeakFinding, sel map[string]bool) bool {
	orphans := sel["orphans"] && len(r.Orphans) > 0
	broken := sel["broken"] && len(r.BrokenLinks) > 0
	untracked := sel["untracked"] && len(r.Untracked) > 0
	frontmatter := sel["frontmatter"] && len(r.FrontmatterFindings) > 0
	edges := sel["edges"] && (len(r.BrokenEdges) > 0 || len(r.EdgeCycles) > 0)
	leaksFound := sel["leaks"] && len(leaks) > 0
	disconnected := sel["disconnected"] && len(r.Disconnected) > 0
	if !orphans && !broken && !untracked && !frontmatter && !edges && !leaksFound && !disconnected {
		fmt.Fprintf(w, "docgraph: clean ✓ (%d tracked .md, %d reachable, 0 findings)\n", r.TrackedMD, r.Reachable)
		return false
	}

	fmt.Fprintln(w, "docgraph — enforces agent-facing repo hygiene across two doc graphs: the")
	fmt.Fprintln(w, "content graph (findability — orphans flags an island no prose reference reaches)")
	fmt.Fprintln(w, "and the metadata graph (structure — disconnected flags a frontmatter doc with no")
	fmt.Fprintln(w, "doc→doc edge). Frontmatter is required on every doc except README. Plus broken")
	fmt.Fprintln(w, "typed-edge/link targets, untracked .md, and a content scan for leak patterns.")
	fmt.Fprintln(w, "All checks run by default; exclude one with --skip. Reads the doc graph and file content.")
	fmt.Fprintf(w, "roots: %v   tracked .md: %d   reachable: %d\n\n", r.Roots, r.TrackedMD, r.Reachable)

	if orphans {
		fmt.Fprintf(w, "ORPHANS (%d) — docs unreachable by link/path-following:\n", len(r.Orphans))
		for _, o := range r.Orphans {
			fmt.Fprintf(w, "  %s\n", o)
		}
		fmt.Fprintln(w)
	}
	if broken {
		fmt.Fprintf(w, "BROKEN LINKS (%d) — .md targets that don't exist:\n", len(r.BrokenLinks))
		for _, b := range r.BrokenLinks {
			fmt.Fprintf(w, "  %s:%d → %s\n", b.Source, b.Line, b.Target)
		}
		fmt.Fprintln(w)
	}
	if untracked {
		fmt.Fprintf(w, "UNTRACKED (%d) — .md on disk but not in git:\n", len(r.Untracked))
		for _, u := range r.Untracked {
			fmt.Fprintf(w, "  %s\n", u)
		}
		fmt.Fprintln(w)
	}
	if frontmatter {
		fmt.Fprintf(w, "FRONTMATTER (%d) — malformed frontmatter or missing required `type`:\n", len(r.FrontmatterFindings))
		for _, f := range r.FrontmatterFindings {
			fmt.Fprintf(w, "  %s → %s\n", f.File, f.Detail)
		}
		fmt.Fprintln(w)
	}
	if edges {
		fmt.Fprintf(w, "EDGES (%d) — frontmatter typed edges with a missing target, or a part-of/supersedes cycle:\n", len(r.BrokenEdges)+len(r.EdgeCycles))
		for _, e := range r.BrokenEdges {
			fmt.Fprintf(w, "  %s [%s] → %s (%s)\n", e.Source, e.Rel, e.Target, e.Reason)
		}
		for _, cyc := range r.EdgeCycles {
			fmt.Fprintf(w, "  cycle: %s → %s\n", strings.Join(cyc, " → "), cyc[0])
		}
		fmt.Fprintln(w)
	}
	if leaksFound {
		fmt.Fprintf(w, "LEAKS (%d) — tree content matching a leak pattern:\n", len(leaks))
		for _, l := range leaks {
			fmt.Fprintf(w, "  %s:%d → %s  (%s)\n", l.File, l.Line, l.Match, l.Pattern)
		}
		fmt.Fprintln(w)
	}
	if disconnected {
		fmt.Fprintf(w, "DISCONNECTED (%d) — frontmatter docs with no doc→doc edge (metadata island):\n", len(r.Disconnected))
		for _, d := range r.Disconnected {
			fmt.Fprintf(w, "  %s\n", d)
		}
		fmt.Fprintln(w)
	}

	n := 0
	if sel["orphans"] {
		n += len(r.Orphans)
	}
	if sel["broken"] {
		n += len(r.BrokenLinks)
	}
	if sel["untracked"] {
		n += len(r.Untracked)
	}
	if sel["frontmatter"] {
		n += len(r.FrontmatterFindings)
	}
	if sel["edges"] {
		n += len(r.BrokenEdges) + len(r.EdgeCycles)
	}
	if sel["leaks"] {
		n += len(leaks)
	}
	if sel["disconnected"] {
		n += len(r.Disconnected)
	}
	printFailureFooter(w, n, orphans, broken, untracked, frontmatter, edges, leaksFound, disconnected)
	return true
}

// printFailureFooter explains, in plain text, why docgraph is exiting non-zero
// and how to act on it — so nobody has to reverse-engineer the gate from a bare
// "failed to push some refs". Only the fix lines for checks that actually have
// findings are shown.
func printFailureFooter(w io.Writer, n int, orphans, broken, untracked, frontmatter, edges, leaks, disconnected bool) {
	bar := strings.Repeat("─", 82)
	fmt.Fprintln(w, bar)
	fmt.Fprintf(w, "docgraph: %d finding(s) in gated checks → exiting non-zero.\n", n)
	fmt.Fprintln(w, "Its intended use is a pre-push gate, so if a git push just failed, this is why: the")
	fmt.Fprintln(w, "non-zero exit aborted the push. A finding is a repo-hygiene problem — a doc an agent")
	fmt.Fprintln(w, "can't reach, a dead .md link, an untracked .md, malformed/incomplete frontmatter, a")
	fmt.Fprintln(w, "frontmatter edge pointing at a missing target, a configured leak pattern matched in")
	fmt.Fprintln(w, "tracked content, or a frontmatter doc with no place in the metadata structure.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fix the findings listed above:")
	if orphans {
		fmt.Fprintln(w, "  ORPHAN    → link it in from a reachable doc; or `--ignore '<glob>'` (a")
		fmt.Fprintln(w, "              .docgraphignore entry) if it is intentionally standalone; or delete it.")
	}
	if broken {
		fmt.Fprintln(w, "  BROKEN    → repair or remove the dead .md link at the shown file:line.")
	}
	if untracked {
		fmt.Fprintln(w, "  UNTRACKED → `git add` it; or delete/ignore it.")
	}
	if frontmatter {
		fmt.Fprintln(w, "  FRONTMATTER → add a `---` frontmatter block with a `type:` (every doc except a")
		fmt.Fprintln(w, "                README.md needs one); or fix malformed YAML; or `--skip frontmatter`.")
	}
	if edges {
		fmt.Fprintln(w, "  EDGES     → fix the frontmatter `to:` target (path is repo-root-relative), or remove the edge.")
		fmt.Fprintln(w, "              A cycle means part-of/supersedes edges form a loop — break it.")
	}
	if leaks {
		fmt.Fprintln(w, "  LEAK      → genericise it, remove it, or add an `allow`/`allow_regex` (optionally")
		fmt.Fprintln(w, "              scoped under `[[dir]]`) to your leaks.toml if the match is legitimate.")
	}
	if disconnected {
		fmt.Fprintln(w, "  DISCONNECTED → give the doc a frontmatter part-of/see-also/depends-on edge to a")
		fmt.Fprintln(w, "                related doc (a covers→code edge does not count); or `--skip disconnected`.")
	}
	fmt.Fprintln(w, bar)
}

// runFootgunDrift checks only footgun declarations ADDED in a range. With
// --range it uses that range; otherwise it reads pre-push ref lines from stdin
// (`<localref> <localsha> <remoteref> <remotesha>`), deriving remotesha..localsha
// per ref (a new branch — zero remotesha — falls back to the closest base).
func runFootgunDrift(args []string, stdout, stderr io.Writer) int {
	if os.Getenv("DOCGRAPH_FOOTGUN_OFF") != "" {
		return 0
	}
	fs := flag.NewFlagSet("docgraph footgun-drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rangeFlag := fs.String("range", "", "explicit base..head to check (else read pre-push stdin)")
	positional, perr := parseArgs(fs, args)
	if perr != nil {
	}
	path := "."
	if len(positional) > 0 {
		path = positional[0]
	}
	root, err := audit.GitRoot(path)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: not a git repository: %s\n", path)
		return 2
	}
	var ranges []audit.RevRange
	if *rangeFlag != "" {
		b, h, ok := splitRange(*rangeFlag)
		if !ok {
			fmt.Fprintf(stderr, "docgraph: bad --range %q (want base..head)\n", *rangeFlag)
			return 2
		}
		ranges = []audit.RevRange{{Base: b, Head: h}}
	} else {
		ranges = rangesFromPrePushStdin(os.Stdin, root)
	}
	if len(ranges) == 0 {
		return 0
	}
	findings, err := audit.FootgunDrift(root, ranges)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if len(findings) == 0 {
		return 0
	}
	printFootgunDrift(stdout, findings)
	// Advisory, NOT a gate: exit 0 so a footgun declaration never aborts the push.
	// docgraph can't judge whether an added declaration is a real footgun, so it
	// nags (the two-question test) and trusts the pusher to double-check — rather
	// than blocking on something it can't evaluate and training a --no-verify habit.
	return 0
}

func splitRange(s string) (string, string, bool) {
	i := strings.Index(s, "..")
	if i < 0 {
		return "", "", false
	}
	b, h := s[:i], s[i+2:]
	if b == "" || h == "" {
		return "", "", false
	}
	return b, h, true
}

const zeroSHA = "0000000000000000000000000000000000000000"

// rangesFromPrePushStdin parses git's pre-push stdin into ranges. Deletions
// (zero local sha) are skipped; a new branch (zero remote sha) falls back to the
// closest base.
func rangesFromPrePushStdin(r io.Reader, root string) []audit.RevRange {
	var out []audit.RevRange
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		localSHA, remoteSHA := f[1], f[3]
		if localSHA == zeroSHA {
			continue // deletion
		}
		if remoteSHA == zeroSHA {
			if base, ok := audit.ClosestBase(root, localSHA); ok {
				out = append(out, audit.RevRange{Base: base, Head: localSHA})
			}
			continue
		}
		out = append(out, audit.RevRange{Base: remoteSHA, Head: localSHA})
	}
	return out
}

// printFootgunDrift renders findings with the two-question remediation. This is
// advisory (the caller exits 0): the message exists to prompt a double-check, not
// to justify a block.
// footgunEchoRunes caps how much of a declaration line is echoed. The file:line
// is what makes a finding actionable — you open the file to judge it, since no
// terminal echo tells you whether a stated rationale is real. The line itself is
// only there to say WHICH declaration, so it is trimmed to a recognisable head.
// Untrimmed it dominated the output: on one real push, nine findings echoed 6.1k
// characters of CLAUDE.md prose (single lines up to 2k) around 270 characters of
// actual payload — a per-push tax on the reader, and on an agent's context when
// the agent is the one pushing.
const footgunEchoRunes = 100

// echoLine trims a declaration line to footgunEchoRunes, rune-safe.
func echoLine(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= footgunEchoRunes {
		return string(r)
	}
	return string(r[:footgunEchoRunes]) + "…"
}

func printFootgunDrift(w io.Writer, fs []audit.FootgunFinding) {
	bar := strings.Repeat("─", 82)
	fmt.Fprintf(w, "FOOTGUNS (%d) added in this push — ADVISORY, the push was NOT blocked. Verify each\n", len(fs))
	fmt.Fprintln(w, "is a real footgun (a trap hit, a tempting-but-wrong path, or a re-litigated")
	fmt.Fprintln(w, "decision, recorded WITH its why) and at the right level — invariant → CLAUDE.md,")
	fmt.Fprintln(w, "deep rationale → docs/, human prose → README. Open each to judge it:")
	for _, f := range fs {
		fmt.Fprintf(w, "  %s:%d → %s\n", f.File, f.Line, echoLine(f.Text))
	}
	fmt.Fprintln(w, "Reword any note-just-in-case as a plain note, or drop it — a follow-up commit is")
	fmt.Fprintln(w, "fine, docgraph did not hold the push.")
	fmt.Fprintln(w, bar)
}

// runCoversDrift is the diff-scoped ADVISORY pre-push subcommand: it nags when a
// pushed range changed code a doc declares it `covers` while that doc went
// untouched. Always exits 0 on a finding — it judges nothing (it cannot tell
// whether the doc actually needed reconciling), so it must not block. Contrast
// doc-drift, which blocks on mechanical staleness.
//
// It lives at pre-push rather than in the doc-drift Stop hook because a Stop hook
// has no channel that is both advisory and agent-visible: its exit-0 stdout
// reaches only the debug log, while every channel that DOES reach the agent (a
// blocking decision, or `hookSpecificOutput.additionalContext`) keeps the turn
// from ending, under the same loop protections as `decision: block`. That is
// structural — a Stop hook exists to decide whether to stop — so an advisory
// check cannot live there. Pre-push is where advisory already works; it is how
// footgun-drift operates.
func runCoversDrift(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Getenv("DOCGRAPH_COVERS_OFF") != "" {
		return 0
	}
	fs := flag.NewFlagSet("docgraph covers-drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rangeFlag := fs.String("range", "", "explicit base..head to check (else read pre-push stdin)")
	positional, perr := parseArgs(fs, args)
	if perr != nil {
	}
	path := "."
	if len(positional) > 0 {
		path = positional[0]
	}
	root, err := audit.GitRoot(path)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: not a git repository: %s\n", path)
		return 2
	}
	var ranges []audit.RevRange
	if *rangeFlag != "" {
		b, h, ok := splitRange(*rangeFlag)
		if !ok {
			fmt.Fprintf(stderr, "docgraph: bad --range %q (want base..head)\n", *rangeFlag)
			return 2
		}
		ranges = []audit.RevRange{{Base: b, Head: h}}
	} else {
		ranges = rangesFromPrePushStdin(stdin, root)
	}
	if len(ranges) == 0 {
		return 0
	}
	// An error from either call is a TOOL error, not a finding: stderr + exit 2.
	// The advisory-never-blocks rule governs findings, not bugs — swallowing a
	// genuine failure would hide it forever. The hook line's `|| true` still keeps
	// even this from aborting a push.
	docs, err := audit.RepoDocs(root, nil)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	findings, err := audit.CoversDrift(root, ranges, docs)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if len(findings) == 0 {
		return 0
	}
	printCoversDrift(stdout, findings)
	return 0
}

// runDocDrift is the Stop-hook subcommand: it flags dangling doc references and
// anchored value drift over the branch's working-tree-inclusive diff, and BLOCKS
// the Stop (exit 2, message on stderr) on any finding. Contrast footgun-drift,
// which is advisory. Bare invocation resolves the diff base and applies the
// once-per-HEAD loop-guard; --range runs a deterministic, guard-free check for
// manual use.
func runDocDrift(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Getenv("DOC_DRIFT_OFF") != "" {
		return 0
	}
	fs := flag.NewFlagSet("docgraph doc-drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rangeFlag := fs.String("range", "", "explicit git-diff spec (base, base..head) — bypasses the loop-guard")
	positional, perr := parseArgs(fs, args)
	if perr != nil {
	}
	path := "."
	if len(positional) > 0 {
		path = positional[0]
	}
	// Drain the Stop payload (bare mode sends JSON on stdin); unused.
	_, _ = io.Copy(io.Discard, stdin)

	root, err := audit.GitRoot(path)
	if err != nil {
		return 0 // not a work-tree (e.g. bare dotfiles repo) -> no-op
	}

	guard := *rangeFlag == ""
	spec := *rangeFlag
	var head string
	if guard {
		h, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil {
			// Unborn HEAD (a freshly `git init`'d repo, no commits yet): there is no
			// commit to diff against, and `git diff HEAD` on one exits 128 — a real
			// git error, not a doc-drift finding. Blocking the Stop on that would gate
			// every turn during repo bootstrap. --range mode is unaffected: an
			// explicit ref is the caller's responsibility.
			return 0
		}
		head = strings.TrimSpace(string(h))
		// Already nagged at this HEAD? Then EVERY path below ends in exit 0 (no
		// findings -> 0; findings -> suppressed by the guard -> 0), so resolving the
		// base and running the diff+greps is pure waste on a hook that fires once
		// per turn. Short-circuit. Exit codes are unchanged by construction — this
		// is the same decision, taken before the work instead of after it.
		if docDriftNaggedAt(root) == head {
			return 0
		}
		spec = docDriftDiffBase(root, head)
	}

	findings, err := audit.DocDrift(root, spec)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if len(findings) == 0 {
		return 0
	}
	if guard {
		docDriftRecordNag(root, head)
	}
	printDocDrift(stderr, findings)
	return 2
}

// docDriftDiffBase resolves what to `git diff` against: the closest integration
// branch's merge-base if this work sits ahead of it, else "HEAD" (on a trunk
// branch the change set IS the working tree — uncommitted only). Memoized per
// (repo, HEAD), because audit.ClosestBase costs a merge-base + a rev-list per
// integration-branch candidate — a dozen git subprocesses, ~85% of a warm
// doc-drift run, re-derived on every Stop hook to recompute an answer that only
// changes when HEAD does.
//
// Staleness is bounded and fails SAFE. The memo can only go stale when an
// integration branch advances while HEAD stays put (i.e. a fetch), and the
// remembered base is then an ANCESTOR of the true one — a superset diff, so
// doc-drift over-reports rather than missing drift. The next commit re-keys it.
func docDriftDiffBase(root, head string) string {
	if base, ok := readDocDriftBase(root, head); ok {
		return base
	}
	spec := "HEAD"
	if base, ok := audit.ClosestBase(root, "HEAD"); ok && base != "" && base != head {
		spec = base
	}
	writeDocDriftBase(root, head, spec)
	return spec
}

// docDriftStatePath is the per-repo state file for suffix — "" is the nag marker,
// ".base" the memoized diff base — or "" when no state dir resolves.
func docDriftStatePath(root, suffix string) string {
	dir := docDriftStateDir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(sum[:])[:16]+suffix)
}

// readDocDriftBase returns the base memoized for head, if the stored entry is
// keyed to that exact HEAD.
func readDocDriftBase(root, head string) (string, bool) {
	p := docDriftStatePath(root, ".base")
	if p == "" {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	h, spec, ok := strings.Cut(strings.TrimSpace(string(b)), " ")
	if !ok || h != head || spec == "" {
		return "", false
	}
	return spec, true
}

// writeDocDriftBase memoizes spec against head. Best-effort: a state dir that
// can't be written just means the next run re-resolves.
func writeDocDriftBase(root, head, spec string) {
	p := docDriftStatePath(root, ".base")
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(head+" "+spec), 0o644)
}

// docDriftNaggedAt returns the HEAD this repo was last nagged at, or "" for
// never (including an unresolvable state dir, which must never suppress).
func docDriftNaggedAt(root string) string {
	p := docDriftStatePath(root, "")
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// docDriftRecordNag records head as nagged, so the repo is nagged at most once
// per (repo, HEAD). Each commit moves HEAD, re-arming — so a blocked Stop can be
// resolved by committing, or by simply stopping again once the finding has been
// judged intentional.
func docDriftRecordNag(root, head string) {
	p := docDriftStatePath(root, "")
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(head), 0o644)
}

// docDriftStateDir resolves $XDG_STATE_HOME/docgraph/doc-drift (default
// ~/.local/state/...), matching the usage-log XDG-state convention.
func docDriftStateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "docgraph", "doc-drift")
}

// printDocDrift renders findings grouped by kind, with the reconcile guidance.
// Self-contained prose — no machine-local path references.
func printDocDrift(w io.Writer, fs []audit.DocDriftFinding) {
	var dangling, value []audit.DocDriftFinding
	for _, f := range fs {
		if f.Kind == audit.Dangling {
			dangling = append(dangling, f)
		} else {
			value = append(value, f)
		}
	}
	fmt.Fprintln(w, "doc-drift: this branch changed code that tracked docs still describe the old way.")
	fmt.Fprintln(w, "Reconcile in THIS change set, or confirm each is intentional history:")
	if len(dangling) > 0 {
		fmt.Fprintln(w, "  Dangling references (symbol deleted, doc still names it):")
		for _, f := range dangling {
			fmt.Fprintf(w, "    • '%s' (definition removed on this branch) still referenced in:\n", f.Symbol)
			for _, h := range f.Hits {
				fmt.Fprintf(w, "        %s:%d: %s\n", h.File, h.Line, h.Text)
			}
		}
	}
	if len(value) > 0 {
		fmt.Fprintln(w, "  Anchored value drift (doc names the constant but shows its old value):")
		for _, f := range value {
			fmt.Fprintf(w, "    • '%s' changed value (old literal %s); doc names the symbol but still shows %s:\n", f.Symbol, f.Old, f.Old)
			for _, h := range f.Hits {
				fmt.Fprintf(w, "        %s:%d: %s\n", h.File, h.Line, h.Text)
			}
		}
	}
	fmt.Fprintln(w, "This catches only anchored/symbol cases — for paraphrased values or reversed")
	fmt.Fprintln(w, "decisions, run a semantic doc sweep before finishing: `docgraph covers <path>`")
	fmt.Fprintln(w, "names the docs that govern a file you changed, including ones no symbol drift")
	fmt.Fprintln(w, "points at. Already reconciled, or is it framed history? Stop again — you won't")
	fmt.Fprintln(w, "be re-prompted for this HEAD.")
}

// coversDriftMessage renders the advisory covers-drift nag.
func coversDriftMessage(fs []audit.CoversFinding) string {
	var b strings.Builder
	b.WriteString("COVERS-DRIFT: this push changes code that a doc declares it covers,\n")
	b.WriteString("but the doc itself is untouched. Reconcile it, or confirm it is still accurate:\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "  • %s covers:\n", f.Doc)
		for _, p := range f.Paths {
			fmt.Fprintf(&b, "      %s\n", p)
		}
	}
	b.WriteString("Advisory — the push is not blocked. Editing the doc silences it.\n")
	return b.String()
}

func printCoversDrift(w io.Writer, fs []audit.CoversFinding) {
	fmt.Fprint(w, coversDriftMessage(fs))
}

// runSchema prints the JSON Schema describing docgraph frontmatter, stamped with
// this build's version. It is how non-owning consumers (compositor, Mycelium)
// obtain the vocabulary they must conform to without re-encoding it. Read-only —
// never part of the pre-push gate.
func runSchema(stdout io.Writer) int {
	stdout.Write(audit.SchemaJSON(version))
	return 0
}

// runCovers prints the docs that document the given repo-root-relative path (via
// a frontmatter `covers` edge, directly or by covering a parent directory). A
// read-only view — never part of the gate.
func runCovers(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docgraph covers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "glob to exclude (repeatable)")
	positional, perr := parseArgs(fs, args)
	if perr != nil {
	}
	if len(positional) < 1 {
		fmt.Fprintln(stderr, "docgraph: usage: docgraph covers <repo-relative-path>")
		return 2
	}
	root, err := audit.GitRoot(".")
	if err != nil {
		fmt.Fprintln(stderr, "docgraph: not a git repository")
		return 2
	}
	docs, err := audit.RepoDocs(root, ignores)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	for _, src := range audit.CoversOf(docs, positional[0]) {
		fmt.Fprintln(stdout, src)
	}
	return 0
}

// runIndex prints a generated markdown index of the doc graph to stdout (redirect
// it to an index.md). A read-only view — never part of the gate.
func runIndex(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docgraph index", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "glob to exclude (repeatable)")
	if _, perr := parseArgs(fs, args); perr != nil {
		return 2
	}
	root, err := audit.GitRoot(".")
	if err != nil {
		fmt.Fprintln(stderr, "docgraph: not a git repository")
		return 2
	}
	docs, err := audit.RepoDocs(root, ignores)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	fmt.Fprint(stdout, audit.IndexMarkdown(docs))
	return 0
}

// runStale prints docs whose `verified` date is older than their staleness
// threshold (per-doc `review:` cadence, else --older-than). A read-only view —
// never part of the gate.
func runStale(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docgraph stale", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "glob to exclude (repeatable)")
	olderThan := fs.Int("older-than", 180, "default staleness threshold in days (a per-doc `review:` cadence overrides it)")
	if _, perr := parseArgs(fs, args); perr != nil {
		return 2
	}
	root, err := audit.GitRoot(".")
	if err != nil {
		fmt.Fprintln(stderr, "docgraph: not a git repository")
		return 2
	}
	docs, err := audit.RepoDocs(root, ignores)
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	for _, s := range audit.StaleDocs(docs, time.Now(), *olderThan) {
		fmt.Fprintf(stdout, "%s (verified %s — %dd old, threshold %dd)\n", s.File, s.Verified, s.AgeDays, s.Threshold)
	}
	return 0
}

// runGraph prints the doc graph — both the content and metadata graphs — as
// human-readable markdown by default, or a stable JSON payload with --json (the
// seam Mycelium ingests). --ref <ref> reads the committed state at that ref from
// the object store instead of the working tree (bare-repo capable). A read-only
// view — never part of the gate; always 0.
func runGraph(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docgraph graph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var roots, ignores multiFlag
	fs.Var(&roots, "root", "extra root doc (repeatable)")
	fs.Var(&ignores, "ignore", "glob to exclude (repeatable)")
	asJSON := fs.Bool("json", false, "emit the graph as JSON (schemaVersion-stamped)")
	ref := fs.String("ref", "", "read the graph from this git ref (e.g. HEAD, dev); works on a bare repo, reads committed state not the working tree")
	if _, perr := parseArgs(fs, args); perr != nil {
		return 2
	}

	var v audit.GraphView
	var err error
	if *ref != "" {
		// Ref mode: read from the object store at *ref via the cwd's git dir.
		// Do NOT call GitRoot — rev-parse --show-toplevel fails on a bare repo.
		v, err = audit.BuildGraphViewAtRef(".", *ref, roots, ignores)
	} else {
		root, gerr := audit.GitRoot(".")
		if gerr != nil {
			fmt.Fprintln(stderr, "docgraph: not a git repository")
			return 2
		}
		v, err = audit.BuildGraphView(root, roots, ignores)
	}
	if err != nil {
		fmt.Fprintf(stderr, "docgraph: %v\n", err)
		return 2
	}
	if *asJSON {
		b, err := v.JSON()
		if err != nil {
			fmt.Fprintf(stderr, "docgraph: %v\n", err)
			return 2
		}
		stdout.Write(b)
		fmt.Fprintln(stdout)
		return 0
	}
	fmt.Fprint(stdout, v.Markdown())
	return 0
}

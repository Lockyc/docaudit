package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultGroup is the implicit group of the top-level terms/regex. A [[dir]]
// ignore glob suppresses this group and no other unless ignore_groups says so,
// which is what lets a private repo silence its own footprint vocabulary while
// staying scanned for terms that must not appear in ANY repo.
const DefaultGroup = "default"

// LeakConfig is the decoded global leaks.toml. Deny terms and their exceptions
// live ONLY here (never in a repo): a committed deny/allow list would re-leak the
// terms it names. Top-level fields apply to every scanned repo; [[group]] sections
// are named deny lists that a dir ignore cannot silence unless it names them;
// [[dir]] sections scope exceptions to files under an absolute path.
type LeakConfig struct {
	Terms      []string    `toml:"terms"`       // literal, case-insensitive deny (DefaultGroup)
	Regex      []string    `toml:"regex"`       // regexp deny (case-insensitive like terms; opt out per-pattern with (?-i))
	Allow      []string    `toml:"allow"`       // literal global allow
	AllowRegex []string    `toml:"allow_regex"` // regexp global allow (also case-insensitive)
	Group      []GroupRule `toml:"group"`       // named deny lists, suppressible only by name
	Dir        []DirRule   `toml:"dir"`         // per-directory exceptions
}

// GroupRule is a named deny list. Its rules deny exactly like the top-level ones;
// the name exists so a [[dir]] can suppress the class in one line instead of
// restating its terms — a restatement that silently drifts as the class grows.
type GroupRule struct {
	Name  string   `toml:"name"`  // required, non-empty, unique, never DefaultGroup
	Terms []string `toml:"terms"` // literal, case-insensitive deny
	Regex []string `toml:"regex"` // regexp deny
}

// DirRule scopes exceptions to files whose absolute path is under Path.
type DirRule struct {
	Path         string   `toml:"path"`          // absolute directory key (a leading ~/ is expanded)
	Ignore       []string `toml:"ignore"`        // path globs (relative to Path) to skip
	IgnoreGroups []string `toml:"ignore_groups"` // which groups Ignore silences; absent means [DefaultGroup]
	Allow        []string `toml:"allow"`         // literal allow, scoped to this subtree
	AllowRegex   []string `toml:"allow_regex"`   // regexp allow, scoped to this subtree
}

// LeakFinding is one deny match not covered by an allow span.
type LeakFinding struct {
	File    string
	Line    int
	Match   string
	Pattern string
}

// matcher is a compiled deny/allow term. Both literal and regex terms compile
// case-insensitively (a leak must be caught in any casing); raw is the source
// string, shown in findings; group is the deny class the rule belongs to (empty
// for allow matchers, which are never group-scoped).
type matcher struct {
	re    *regexp.Regexp
	raw   string
	group string
}

func literalMatcher(s string) (matcher, bool) {
	if strings.TrimSpace(s) == "" {
		return matcher{}, false
	}
	return matcher{re: regexp.MustCompile("(?i)" + regexp.QuoteMeta(s)), raw: s}, true
}

// regexMatcher compiles a user regexp case-insensitively by default: in a
// leak-prevention gate a false negative from a casing mismatch (a footprint term
// written `SecretHost` slipping a `secrethost` pattern) is the cardinal sin, so
// regex matches `terms`' case-folding. A pattern that genuinely needs case
// sensitivity opts out with an inline (?-i).
func regexMatcher(s string) (matcher, bool, error) {
	if strings.TrimSpace(s) == "" {
		return matcher{}, false, nil
	}
	re, err := regexp.Compile("(?i)" + s)
	if err != nil {
		return matcher{}, false, err
	}
	return matcher{re: re, raw: s}, true, nil
}

type compiledDir struct {
	path         string // cleaned absolute
	ignore       []string
	ignoreGroups []string // resolved: never empty, defaults to [DefaultGroup]
	allow        []matcher
}

type compiledLeaks struct {
	deny  []matcher // global terms + regex — the config is the sole source of rules
	allow []matcher // global allow + allow_regex
	dirs  []compiledDir
}

// expandDirPath resolves a [[dir]] path key to a cleaned absolute path, expanding
// a leading ~/ to the home dir. A non-absolute path is a config error, not a
// silent no-op: dir-scoping matches by absolute-path containment, so a relative
// or unexpanded-~ path would quietly never apply — and a silently-dead exclusion
// in a gate is exactly what trains people to reach for --no-verify.
func expandDirPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("must be an absolute path")
	}
	return p, nil
}

// compile turns a LeakConfig into matchers. Literal entries never error; a bad
// regexp in any regex field, a malformed [[group]], a non-absolute [[dir]] path,
// or an unusable ignore_groups is a fatal config error — a silently-dead rule in
// a gate is exactly what trains people to reach for --no-verify.
func (c LeakConfig) compile() (compiledLeaks, error) {
	var cl compiledLeaks
	addLit := func(dst *[]matcher, ss []string, group string) {
		for _, s := range ss {
			if m, ok := literalMatcher(s); ok {
				m.group = group
				*dst = append(*dst, m)
			}
		}
	}
	addRe := func(dst *[]matcher, ss []string, group, what string) error {
		for _, s := range ss {
			m, ok, err := regexMatcher(s)
			if err != nil {
				return fmt.Errorf("%s %q: %v", what, s, err)
			}
			if ok {
				m.group = group
				*dst = append(*dst, m)
			}
		}
		return nil
	}
	addLit(&cl.deny, c.Terms, DefaultGroup)
	if err := addRe(&cl.deny, c.Regex, DefaultGroup, "leaks regex"); err != nil {
		return compiledLeaks{}, err
	}
	defined := map[string]bool{DefaultGroup: true}
	for _, g := range c.Group {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return compiledLeaks{}, fmt.Errorf("leaks [[group]]: name is required")
		}
		if name == DefaultGroup {
			return compiledLeaks{}, fmt.Errorf("leaks [[group]] %q: name is reserved for the top-level terms/regex", name)
		}
		if defined[name] {
			return compiledLeaks{}, fmt.Errorf("leaks [[group]] %q: duplicate group name", name)
		}
		defined[name] = true
		addLit(&cl.deny, g.Terms, name)
		if err := addRe(&cl.deny, g.Regex, name, fmt.Sprintf("leaks [[group]] %q regex", name)); err != nil {
			return compiledLeaks{}, err
		}
	}
	addLit(&cl.allow, c.Allow, "")
	if err := addRe(&cl.allow, c.AllowRegex, "", "leaks allow_regex"); err != nil {
		return compiledLeaks{}, err
	}
	for _, d := range c.Dir {
		path, err := expandDirPath(d.Path)
		if err != nil {
			return compiledLeaks{}, fmt.Errorf("leaks [[dir]] path %q: %v", d.Path, err)
		}
		groups := d.IgnoreGroups
		if len(groups) == 0 {
			groups = []string{DefaultGroup}
		} else if len(d.Ignore) == 0 {
			return compiledLeaks{}, fmt.Errorf("leaks [[dir]] %q: ignore_groups set with no ignore globs — it would never apply", d.Path)
		}
		for _, g := range groups {
			if !defined[g] {
				return compiledLeaks{}, fmt.Errorf("leaks [[dir]] %q: ignore_groups names undefined group %q", d.Path, g)
			}
		}
		cd := compiledDir{path: path, ignore: d.Ignore, ignoreGroups: groups}
		addLit(&cd.allow, d.Allow, "")
		if err := addRe(&cd.allow, d.AllowRegex, "", fmt.Sprintf("leaks [[dir]] %q allow_regex", d.Path)); err != nil {
			return compiledLeaks{}, err
		}
		cl.dirs = append(cl.dirs, cd)
	}
	return cl, nil
}

// looksBinary reports whether a head chunk contains a NUL byte.
func looksBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// relUnder reports whether abs is within dir (or equals it) and returns abs
// relative to dir as a slash path, for glob matching.
func relUnder(abs, dir string) (string, bool) {
	if abs == dir {
		return ".", true
	}
	if !strings.HasPrefix(abs, dir+string(os.PathSeparator)) {
		return "", false
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// LeakScan walks every git-tracked, non-binary file and reports deny matches not
// covered by an allow span. Scope is git tracking (a tracked file ships publicly),
// not the doc-graph ignore layers. extraIgnores (--ignore CLI globs) drop a file
// entirely; an applicable [[dir]].ignore drops only the groups that dir names
// (DefaultGroup unless ignore_groups says otherwise), so a blanket ignore silences
// a repo's own footprint vocabulary without blinding the scan to terms that must
// not appear anywhere. Global + dir-scoped allows suppress individual matches.
// History is never read. A bad regexp in the config is an error.
func LeakScan(repoRoot string, cfg LeakConfig, extraIgnores []string) ([]LeakFinding, error) {
	cl, err := cfg.compile()
	if err != nil {
		return nil, err
	}
	files, err := gitLines(repoRoot, "ls-files")
	if err != nil {
		return nil, err
	}
	var findings []LeakFinding
	for _, f := range files {
		if matchesIgnore(f, extraIgnores) {
			continue
		}
		abs := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(f)))
		var dirAllows []matcher
		suppressed := map[string]bool{}
		for _, d := range cl.dirs {
			rel, under := relUnder(abs, d.path)
			if !under {
				continue
			}
			if matchesIgnore(rel, d.ignore) {
				for _, g := range d.ignoreGroups {
					suppressed[g] = true
				}
			}
			dirAllows = append(dirAllows, d.allow...)
		}
		deny := cl.deny
		if len(suppressed) > 0 {
			deny = nil
			for _, m := range cl.deny {
				if !suppressed[m.group] {
					deny = append(deny, m)
				}
			}
			// Every group suppressed — skip before the read, preserving the fast
			// path a blanket `ignore = ["**"]` has always had.
			if len(deny) == 0 {
				continue
			}
		}
		allow := cl.allow
		if len(dirAllows) > 0 {
			allow = append(append([]matcher{}, cl.allow...), dirAllows...)
		}
		b, err := os.ReadFile(abs)
		if err != nil || looksBinary(b) {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			findings = append(findings, scanLine(f, i+1, line, deny, allow)...)
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Match < findings[j].Match
	})
	return findings, nil
}

// scanLine returns findings for one line: deny matches not covered by an allow
// span. A deny span [s,e) is covered iff some allow rule matches [as,ae) with
// as<=s && ae>=e (e.g. `lsjc` inside an allowed `au.lsjc.curator`).
func scanLine(file string, lineNo int, line string, deny, allow []matcher) []LeakFinding {
	var allowSpans [][]int
	for _, a := range allow {
		allowSpans = append(allowSpans, a.re.FindAllStringIndex(line, -1)...)
	}
	covered := func(s, e int) bool {
		for _, sp := range allowSpans {
			if sp[0] <= s && sp[1] >= e {
				return true
			}
		}
		return false
	}
	var out []LeakFinding
	seen := map[string]bool{}
	for _, d := range deny {
		for _, loc := range d.re.FindAllStringIndex(line, -1) {
			if loc[0] == loc[1] { // defensive: skip any zero-width match
				continue
			}
			if covered(loc[0], loc[1]) {
				continue
			}
			m := line[loc[0]:loc[1]]
			key := fmt.Sprintf("%d:%s:%s", loc[0], m, d.raw)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, LeakFinding{File: file, Line: lineNo, Match: m, Pattern: d.raw})
		}
	}
	return out
}

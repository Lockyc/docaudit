package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileLeaksLiteralAndRegex(t *testing.T) {
	cfg := LeakConfig{
		Terms:      []string{"nucleus", "", "  "},
		Regex:      []string{`192\.168\.1\.\d+`},
		Allow:      []string{"github.com/lockyc"},
		AllowRegex: []string{`au\.lsjc\.[a-z]+`},
		Dir:        []DirRule{{Path: "/x", Ignore: []string{"v/*.json"}, Allow: []string{"mycelium"}}},
	}
	cl, err := cfg.compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.deny) != 2 {
		t.Errorf("deny = %d, want 2 (1 term + 1 regex; no hidden built-ins — config is the sole source)", len(cl.deny))
	}
	if len(cl.allow) != 2 {
		t.Errorf("allow = %d, want 2", len(cl.allow))
	}
	if len(cl.dirs) != 1 || cl.dirs[0].path != "/x" || len(cl.dirs[0].allow) != 1 {
		t.Errorf("dirs = %+v, want one dir /x with 1 allow", cl.dirs)
	}
}

func TestCompileLeaksBadRegex(t *testing.T) {
	_, err := LeakConfig{Regex: []string{"(unclosed"}}.compile()
	if err == nil || !strings.Contains(err.Error(), "leaks regex") {
		t.Errorf("want a leaks-regex compile error, got %v", err)
	}
}

// J1: a regexp deny is case-insensitive by default — a footprint term written in
// a different casing must NOT slip the gate (false negatives are the cardinal sin).
func TestLeakScanRegexDenyIsCaseInsensitive(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "host Nucleus-Prod here\n"}, []string{"a.md"})
	found, err := LeakScan(dir, LeakConfig{Regex: []string{"nucleus"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("regex deny must be case-insensitive (catch 'Nucleus'), got %+v", found)
	}
}

// J1: allow_regex is case-insensitive too, so it suppresses a deny match whose
// casing differs from the allow pattern.
func TestLeakScanAllowRegexIsCaseInsensitive(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "id au.LSJC.curator ok\n"}, []string{"a.md"})
	found, err := LeakScan(dir, LeakConfig{Terms: []string{"lsjc"}, AllowRegex: []string{`au\.lsjc\.[a-z]+`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("case-insensitive allow_regex should suppress the match, got %+v", found)
	}
}

// J2: a non-absolute [[dir]] path is a fatal config error, not a silent no-op.
func TestCompileDirPathRelativeIsError(t *testing.T) {
	_, err := LeakConfig{Dir: []DirRule{{Path: "relative/dir"}}}.compile()
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("a non-absolute [[dir]] path must be a config error, got %v", err)
	}
}

// J2: a leading ~/ in a [[dir]] path expands to the home dir.
func TestCompileDirPathTildeExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cl, err := LeakConfig{Dir: []DirRule{{Path: "~/proj"}}}.compile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "proj")
	if len(cl.dirs) != 1 || cl.dirs[0].path != want {
		t.Errorf("~ must expand to %q, got %+v", want, cl.dirs)
	}
}

func TestLeakScanLiteralAndRegex(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		"README.md": "clean\ncontact Lachlan here\nhost nucleus up\n", // case-insensitive literal + literal
		"net.conf":  "ip 192.168.1.42 assigned\n",                     // regex
	}, []string{"README.md", "net.conf"})

	cfg := LeakConfig{Terms: []string{"lachlan", "nucleus"}, Regex: []string{`192\.168\.1\.\d+`}}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("findings = %+v, want 3 (Lachlan, nucleus, IP)", found)
	}
}

func TestLeakScanGlobalAllowSuppresses(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		"a.md": "bundle au.lsjc.curator is fine\nbut lsjc.au alone leaks\n",
	}, []string{"a.md"})

	cfg := LeakConfig{Terms: []string{"lsjc"}, Allow: []string{"au.lsjc.curator"}}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// line 1 'lsjc' covered by the allow span; line 2 'lsjc' flagged.
	if len(found) != 1 || found[0].Line != 2 {
		t.Fatalf("findings = %+v, want exactly a.md:2", found)
	}
}

// The config is the sole source of rules: an empty config scans nothing, even a
// secret-shaped string — there are no hidden built-in patterns.
func TestLeakScanEmptyConfigScansNothing(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		"secrets.env": "AWS=AKIAIOSFODNN7EXAMPLE\nGH=ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8\n",
	}, []string{"secrets.env"})
	found, err := LeakScan(dir, LeakConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("an empty config must scan nothing (no hidden built-ins), got %+v", found)
	}
}

func TestLeakScanDirIgnoreDropsFile(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		"vendor/spec.json": "key AKIAIOSFODNN7EXAMPLE inside\n", // dropped by dir ignore
		"src.go":           "key AKIAIOSFODNN7EXAMPLE inside\n", // flagged
	}, []string{"vendor/spec.json", "src.go"})

	// The rule comes from the config, not a built-in.
	cfg := LeakConfig{
		Regex: []string{`(?-i)AKIA[0-9A-Z]{16}`},
		Dir:   []DirRule{{Path: dir, Ignore: []string{"vendor/*.json"}}},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].File != "src.go" {
		t.Fatalf("findings = %+v, want only src.go (vendor/spec.json ignored)", found)
	}
}

func TestLeakScanDirAllowIsScoped(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		"sub/a.md": "mycelium here\n", // suppressed: under the dir allow
		"top.md":   "mycelium here\n", // flagged: outside the dir
	}, []string{"sub/a.md", "top.md"})

	cfg := LeakConfig{
		Terms: []string{"mycelium"},
		Dir:   []DirRule{{Path: dir + "/sub", Allow: []string{"mycelium"}}},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].File != "top.md" {
		t.Fatalf("findings = %+v, want only top.md (sub/ allow-scoped)", found)
	}
}

func TestLeakScanTrackedToolingStillScanned(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		".claude/skills/foo.md": "internal note: nucleus\n",
		".docgraphignore":       ".claude/**\n",
	}, []string{".claude/skills/foo.md", ".docgraphignore"})

	found, err := LeakScan(dir, LeakConfig{Terms: []string{"nucleus"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var hit bool
	for _, f := range found {
		if f.File == ".claude/skills/foo.md" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("tracked .claude/ must stay in leak scope despite doc-graph ignores, got %+v", found)
	}
}

func TestLeakScanBinarySkippedAndExtraIgnore(t *testing.T) {
	dir := setupRepo(t, map[string]string{
		"logo.bin":  "\x00\x01nucleus\x00", // binary, skipped
		"vendor.md": "nucleus here\n",      // dropped by --ignore
		"real.md":   "nucleus here\n",      // flagged
	}, []string{"logo.bin", "vendor.md", "real.md"})

	found, err := LeakScan(dir, LeakConfig{Terms: []string{"nucleus"}}, []string{"vendor.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].File != "real.md" {
		t.Fatalf("findings = %+v, want only real.md", found)
	}
}

func TestLeakScanBadRegexIsError(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "x\n"}, []string{"a.md"})
	_, err := LeakScan(dir, LeakConfig{Regex: []string{"(unclosed"}}, nil)
	if err == nil {
		t.Error("bad regex in config should make LeakScan error")
	}
}

// A [[group]] block is a named deny list; its matchers carry the group name so a
// dir ignore glob can suppress the default group without touching it.
func TestCompileLeaksNamedGroups(t *testing.T) {
	cfg := LeakConfig{
		Terms: []string{"nucleus"},
		Group: []GroupRule{{
			Name:  "client",
			Terms: []string{"acme"},
			Regex: []string{`acme[\s_-]*corp`},
		}},
	}
	cl, err := cfg.compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.deny) != 3 {
		t.Fatalf("deny = %d, want 3 (1 default term + 1 group term + 1 group regex)", len(cl.deny))
	}
	got := map[string]int{}
	for _, m := range cl.deny {
		got[m.group]++
	}
	if got[DefaultGroup] != 1 || got["client"] != 2 {
		t.Errorf("group tags = %v, want 1 default + 2 client", got)
	}
}

// `default` names the top-level terms/regex, so a [[group]] claiming it is a
// fatal config error rather than a silent shadow.
func TestCompileLeaksGroupNameDefaultReserved(t *testing.T) {
	_, err := LeakConfig{Group: []GroupRule{{Name: "default", Terms: []string{"x"}}}}.compile()
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("want a reserved-name error, got %v", err)
	}
}

func TestCompileLeaksGroupNameRequired(t *testing.T) {
	_, err := LeakConfig{Group: []GroupRule{{Terms: []string{"x"}}}}.compile()
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("want a missing-name error, got %v", err)
	}
}

func TestCompileLeaksGroupNameDuplicate(t *testing.T) {
	cfg := LeakConfig{Group: []GroupRule{
		{Name: "client", Terms: []string{"a"}},
		{Name: "client", Terms: []string{"b"}},
	}}
	_, err := cfg.compile()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("want a duplicate-group error, got %v", err)
	}
}

// An empty [[group]] would still register in `defined`, so an ignore_groups
// naming it passes validation while filtering zero matchers — a blanket
// ignore that looks configured but silences nothing.
func TestCompileLeaksGroupEmpty(t *testing.T) {
	cfg := LeakConfig{Group: []GroupRule{{Name: "client"}}}
	_, err := cfg.compile()
	if err == nil || !strings.Contains(err.Error(), "no terms or regex") {
		t.Errorf("want a no-terms-or-regex error, got %v", err)
	}
}

// A dir naming a group that does not exist would silently suppress nothing —
// the same silently-dead-exclusion failure expandDirPath already guards against.
func TestCompileLeaksIgnoreGroupsUndefined(t *testing.T) {
	cfg := LeakConfig{Dir: []DirRule{{
		Path:         "/x",
		Ignore:       []string{"**"},
		IgnoreGroups: []string{"nope"},
	}}}
	_, err := cfg.compile()
	if err == nil || !strings.Contains(err.Error(), "undefined group") {
		t.Errorf("want an undefined-group error, got %v", err)
	}
}

// ignore_groups only ever narrows what an ignore glob suppresses, so setting it
// with no globs is a no-op that looks meaningful.
func TestCompileLeaksIgnoreGroupsWithoutIgnore(t *testing.T) {
	cfg := LeakConfig{
		Group: []GroupRule{{Name: "client", Terms: []string{"acme"}}},
		Dir:   []DirRule{{Path: "/x", IgnoreGroups: []string{"client"}}},
	}
	_, err := cfg.compile()
	if err == nil || !strings.Contains(err.Error(), "no ignore globs") {
		t.Errorf("want a no-globs error, got %v", err)
	}
}

// Absent ignore_groups means the default group, preserving today's meaning of a
// bare ignore glob.
func TestCompileLeaksIgnoreGroupsDefaults(t *testing.T) {
	cfg := LeakConfig{Dir: []DirRule{{Path: "/x", Ignore: []string{"**"}}}}
	cl, err := cfg.compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.dirs) != 1 || len(cl.dirs[0].ignoreGroups) != 1 || cl.dirs[0].ignoreGroups[0] != DefaultGroup {
		t.Errorf("ignoreGroups = %v, want [%q]", cl.dirs[0].ignoreGroups, DefaultGroup)
	}
}

func TestCompileLeaksGroupBadRegex(t *testing.T) {
	cfg := LeakConfig{Group: []GroupRule{{Name: "client", Regex: []string{"(unclosed"}}}}
	_, err := cfg.compile()
	if err == nil || !strings.Contains(err.Error(), `[[group]] "client" regex`) {
		t.Errorf("want a group-regex compile error, got %v", err)
	}
}

// The whole point of groups: a blanket ignore silences the footprint vocabulary
// a private repo legitimately carries, and leaves a cross-boundary term live.
func TestLeakScanIgnoreSuppressesOnlyDefaultGroup(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "host nucleus and client acme\n"}, []string{"a.md"})
	cfg := LeakConfig{
		Terms: []string{"nucleus"},
		Group: []GroupRule{{Name: "client", Terms: []string{"acme"}}},
		Dir:   []DirRule{{Path: dir, Ignore: []string{"**"}}},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Pattern != "acme" {
		t.Fatalf("want only the client-group match to survive the ignore, got %+v", found)
	}
}

// Naming the group in ignore_groups is how you opt a subtree out of it — the
// one-line replacement for restating the class in allow.
func TestLeakScanIgnoreGroupsSuppressesNamedGroup(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "host nucleus and client acme\n"}, []string{"a.md"})
	cfg := LeakConfig{
		Terms: []string{"nucleus"},
		Group: []GroupRule{{Name: "client", Terms: []string{"acme"}}},
		Dir: []DirRule{{
			Path:         dir,
			Ignore:       []string{"**"},
			IgnoreGroups: []string{"client"},
		}},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Pattern != "nucleus" {
		t.Fatalf("want only the default-group match to survive, got %+v", found)
	}
}

// Naming every group is still available and still means "skip this file".
func TestLeakScanIgnoreGroupsAllSuppressed(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "host nucleus and client acme\n"}, []string{"a.md"})
	cfg := LeakConfig{
		Terms: []string{"nucleus"},
		Group: []GroupRule{{Name: "client", Terms: []string{"acme"}}},
		Dir: []DirRule{{
			Path:         dir,
			Ignore:       []string{"**"},
			IgnoreGroups: []string{"default", "client"},
		}},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("want every group suppressed, got %+v", found)
	}
}

// An explicit allow still suppresses a grouped term by naming it — the escape
// hatch for the repo that legitimately owns the vocabulary.
func TestLeakScanDirAllowSuppressesGroupedTerm(t *testing.T) {
	dir := setupRepo(t, map[string]string{"a.md": "client acme here\n"}, []string{"a.md"})
	cfg := LeakConfig{
		Group: []GroupRule{{Name: "client", Terms: []string{"acme"}}},
		Dir:   []DirRule{{Path: dir, Allow: []string{"acme"}}},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("an explicit dir allow must suppress a grouped term, got %+v", found)
	}
}

// A narrower dir's ignore composes with a wider dir's allow instead of cutting
// the loop short, so exceptions from every matching dir apply.
func TestLeakScanIgnoreDoesNotDropOtherDirsAllows(t *testing.T) {
	dir := setupRepo(t, map[string]string{"sub/a.md": "client acme and host nucleus\n"}, []string{"sub/a.md"})
	cfg := LeakConfig{
		Terms: []string{"nucleus"},
		Group: []GroupRule{{Name: "client", Terms: []string{"acme"}}},
		Dir: []DirRule{
			{Path: dir, Allow: []string{"acme"}},
			{Path: filepath.Join(dir, "sub"), Ignore: []string{"**"}},
		},
	}
	found, err := LeakScan(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("parent allow + child default-ignore should leave nothing, got %+v", found)
	}
}

package audit

import (
	"reflect"
	"testing"
)

func TestReplaceTextRulesTermsAreEscapedAndCaseInsensitive(t *testing.T) {
	lines, _ := ReplaceTextRules(LeakConfig{Terms: []string{"secret.host", "PlainTerm"}})
	want := []string{`regex:(?i)secret\.host`, `regex:(?i)PlainTerm`}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got %q, want %q", lines, want)
	}
}

func TestReplaceTextRulesRegexDefaultGetsCaseInsensitivePrefix(t *testing.T) {
	lines, _ := ReplaceTextRules(LeakConfig{Regex: []string{`foo-[0-9]+`}})
	want := []string{`regex:(?i)foo-[0-9]+`}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got %q, want %q", lines, want)
	}
}

func TestReplaceTextRulesRegexOptOutKeepsCaseSensitive(t *testing.T) {
	// A leading (?-i) is docgraph's documented case-sensitive opt-out, but
	// git-filter-repo compiles regex: lines with Python re, which rejects a bare
	// (?-i) flag-clear. The flag must be stripped rather than emitted verbatim,
	// leaving a plain case-sensitive pattern.
	lines, _ := ReplaceTextRules(LeakConfig{Regex: []string{`(?-i)AKIA[0-9A-Z]{16}`}})
	want := []string{`regex:AKIA[0-9A-Z]{16}`}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got %q, want %q", lines, want)
	}
}

func TestReplaceTextRulesDropsExceptionsAndDirsWithCounts(t *testing.T) {
	cfg := LeakConfig{
		Terms:      []string{"tok"},
		Allow:      []string{"tokublic"},
		AllowRegex: []string{`ok-[0-9]+`},
		Dir: []DirRule{
			{Path: "/a", Ignore: []string{"**/*_test.go"}},
			{Path: "/b", Allow: []string{"okhere"}, AllowRegex: []string{`x-\d+`}},
		},
	}
	lines, dropped := ReplaceTextRules(cfg)
	if !reflect.DeepEqual(lines, []string{`regex:(?i)tok`}) {
		t.Errorf("only the deny term should be emitted, got %q", lines)
	}
	// 1 global allow + 1 global allow_regex + 1 dir allow + 1 dir allow_regex = 4
	if dropped.Allows != 4 {
		t.Errorf("dropped.Allows = %d, want 4", dropped.Allows)
	}
	if dropped.Dirs != 2 {
		t.Errorf("dropped.Dirs = %d, want 2", dropped.Dirs)
	}
}

func TestReplaceTextRulesDedupesOrdersAndSkipsEmpty(t *testing.T) {
	cfg := LeakConfig{
		Terms: []string{"a", " ", "a"},  // dup + whitespace-only
		Regex: []string{`z+`, `z+`, ``}, // dup + empty
	}
	lines, _ := ReplaceTextRules(cfg)
	want := []string{`regex:(?i)a`, `regex:(?i)z+`} // terms first, then regex, de-duped
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got %q, want %q", lines, want)
	}
}

func TestValidateRejectsBadRegexAndNonAbsoluteDir(t *testing.T) {
	if err := (LeakConfig{Regex: []string{`([`}}).Validate(); err == nil {
		t.Error("bad regex should fail Validate")
	}
	if err := (LeakConfig{Dir: []DirRule{{Path: "relative/dir"}}}).Validate(); err == nil {
		t.Error("non-absolute [[dir]] path should fail Validate")
	}
	if err := (LeakConfig{Terms: []string{"ok"}, Regex: []string{`z+`}}).Validate(); err != nil {
		t.Errorf("valid config should pass Validate, got %v", err)
	}
}

// A grouped deny rule is still a deny rule: leaks-rules must export it, or a
// history scrub silently skips the class groups exist to protect.
func TestReplaceTextRulesIncludesGroups(t *testing.T) {
	cfg := LeakConfig{
		Terms: []string{"corehost"},
		Group: []GroupRule{{
			Name:  "client",
			Terms: []string{"acme"},
			Regex: []string{`(?-i)ACME[0-9]{4}`},
		}},
	}
	lines, _ := ReplaceTextRules(cfg)
	want := []string{
		"regex:(?i)corehost",
		"regex:(?i)acme",
		"regex:ACME[0-9]{4}",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

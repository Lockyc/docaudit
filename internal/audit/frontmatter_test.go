package audit

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCoreVocabularies(t *testing.T) {
	if !reflect.DeepEqual(CoreTypes, []string{"runbook", "architecture", "reference", "decision", "guide", "index"}) {
		t.Errorf("CoreTypes = %v", CoreTypes)
	}
	if !reflect.DeepEqual(CoreRels, []string{"covers", "part-of", "supersedes", "depends-on", "runbook-for", "see-also", "source"}) {
		t.Errorf("CoreRels = %v", CoreRels)
	}
}

func TestDocDecode(t *testing.T) {
	src := `type: runbook
title: Restore Vaultwarden
description: recovery
tags: [vault, corehost]
verified: 2026-06-30
review: 90d
service: vaultwarden
links:
  - rel: covers
    to: scripts/vault-restore.sh
  - rel: depends-on
    to: docs/services/proxmox.md
    note: needs corehost reachable first
`
	var d Doc
	if err := yaml.Unmarshal([]byte(src), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Type != "runbook" || d.Title != "Restore Vaultwarden" {
		t.Errorf("type/title = %q / %q", d.Type, d.Title)
	}
	if !reflect.DeepEqual(d.Tags, []string{"vault", "corehost"}) {
		t.Errorf("tags = %v", d.Tags)
	}
	if d.Verified != "2026-06-30" || d.Review != "90d" {
		t.Errorf("verified/review = %q / %q", d.Verified, d.Review)
	}
	if len(d.Links) != 2 {
		t.Fatalf("links len = %d, want 2: %v", len(d.Links), d.Links)
	}
	if d.Links[0].Rel != "covers" || d.Links[0].To != "scripts/vault-restore.sh" {
		t.Errorf("links[0] = %+v", d.Links[0])
	}
	if d.Links[1].Note != "needs corehost reachable first" {
		t.Errorf("links[1].Note = %q", d.Links[1].Note)
	}
	if d.Extra["service"] != "vaultwarden" {
		t.Errorf("Extra[service] = %v, want vaultwarden", d.Extra["service"])
	}
}

func TestSplitFrontmatter(t *testing.T) {
	fm, body, has := SplitFrontmatter("---\ntype: runbook\n---\n# Body\ntext\n")
	if !has {
		t.Fatal("has = false, want true")
	}
	if fm != "type: runbook" {
		t.Errorf("fm = %q", fm)
	}
	if body != "# Body\ntext\n" {
		t.Errorf("body = %q", body)
	}
}

func TestSplitFrontmatterNone(t *testing.T) {
	_, _, has := SplitFrontmatter("# Just a doc\nno frontmatter\n")
	if has {
		t.Error("has = true, want false for a doc with no leading --- block")
	}
	// A --- that is not on the first line is a horizontal rule, not frontmatter.
	if _, _, has := SplitFrontmatter("intro\n---\nnot frontmatter\n"); has {
		t.Error("mid-file --- treated as frontmatter")
	}
}

func TestParseFrontmatterNoneIsNilNil(t *testing.T) {
	d, err := ParseFrontmatter("# plain\n")
	if err != nil || d != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", d, err)
	}
}

func TestParseFrontmatterMalformed(t *testing.T) {
	d, err := ParseFrontmatter("---\ntype: [unterminated\n---\n")
	if err == nil {
		t.Fatal("err = nil, want a YAML decode error")
	}
	if d != nil {
		t.Errorf("d = %v, want nil on error", d)
	}
}

func TestParseFrontmatterOK(t *testing.T) {
	d, err := ParseFrontmatter("---\ntype: runbook\nlinks:\n  - rel: covers\n    to: x.sh\n---\nbody\n")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if d == nil || d.Type != "runbook" || len(d.Links) != 1 || d.Links[0].To != "x.sh" {
		t.Fatalf("d = %+v", d)
	}
}

func TestFirstHeading(t *testing.T) {
	cases := map[string]string{
		"# Title\n\nbody\n":                      "Title",
		"\n\n#   Spaced   \nbody":                "Spaced",
		"intro prose\n# Later\n":                 "Later",
		"## Only H2\n":                           "",
		"no heading at all\n":                    "",
		"#NoSpace\n":                             "",
		"```sh\n# not a heading\n```\n# Real\n":  "Real",
		"~~~\n# fenced\n~~~\n":                   "",
		"    # indented is a code block\n":       "",
		"```sh\n# unclosed fence swallows all\n": "",
	}
	for body, want := range cases {
		if got := firstHeading(body); got != want {
			t.Errorf("firstHeading(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestParseFrontmatterHeadingFromBody(t *testing.T) {
	// Heading is derived from the body, never a frontmatter key — a doc must not
	// restate its own H1.
	d, err := ParseFrontmatter("---\ntype: guide\n---\n# Real Title\n\nbody\n")
	if err != nil || d == nil {
		t.Fatalf("got (%v, %v)", d, err)
	}
	if d.Heading != "Real Title" {
		t.Errorf("Heading = %q, want %q", d.Heading, "Real Title")
	}
	// A `heading:` key in the frontmatter must not populate it (nor be rejected —
	// unknown keys land in Extra).
	d2, err := ParseFrontmatter("---\ntype: guide\nheading: injected\n---\n# Body Wins\n")
	if err != nil || d2 == nil {
		t.Fatalf("got (%v, %v)", d2, err)
	}
	if d2.Heading != "Body Wins" {
		t.Errorf("Heading = %q, want the body H1 to win over a `heading:` key", d2.Heading)
	}
}

func TestSplitFrontmatterNoClose(t *testing.T) {
	// Opening --- with no closing --- is NOT frontmatter (a lone fence line).
	fm, body, has := SplitFrontmatter("---\ntype: runbook\nno close here\n")
	if has {
		t.Errorf("has = true for an unterminated block; want false")
	}
	if fm != "" {
		t.Errorf("fm = %q, want empty", fm)
	}
	if body != "---\ntype: runbook\nno close here\n" {
		t.Errorf("body = %q, want the original content", body)
	}
}

func TestParseFrontmatterEmptyBlock(t *testing.T) {
	// A well-formed but empty block yields a non-nil, zero-value Doc, no error.
	d, err := ParseFrontmatter("---\n---\nbody\n")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if d == nil {
		t.Fatal("d = nil, want a non-nil zero-value Doc")
	}
	if d.Type != "" {
		t.Errorf("d.Type = %q, want empty", d.Type)
	}
}

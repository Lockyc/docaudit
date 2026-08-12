# docgraph

[![Release](https://img.shields.io/github/v/release/Lockyc/docgraph?sort=semver&label=release)](https://github.com/lockyc/docgraph/releases/latest)
[![CI](https://github.com/lockyc/docgraph/actions/workflows/ci.yml/badge.svg)](https://github.com/lockyc/docgraph/actions/workflows/ci.yml)
![Built with Go](https://img.shields.io/badge/built%20with-Go-00ADD8?logo=go&logoColor=white)
[![License](https://img.shields.io/github/license/Lockyc/docgraph)](LICENSE)

Audits a repo's **agent-facing documentation graph** — the docs an AI agent
navigates by grep and by following `[x](y.md)` links, not the rendered site a
human browses — and scans tracked file content for leaked secret / owner-specific
strings. It's built to run as a **pre-push gate** or in CI without a wrapper: a
finding exits non-zero and aborts the push.

## Quick start

```bash
go install github.com/lockyc/docgraph/v3@latest   # needs Go + git on PATH
docgraph .                                      # audit the current repo
docgraph install-hook                           # wire it in as a pre-push gate
```

Under Claude Code, `/docgraph:install` does the install, wires the `doc-drift`
Stop hook, and seeds the leaks config for you. See [Install](#install) for the
full menu.

## The four modes

docgraph has four independent modes, each with its own trigger and scope:

| Mode | Command | Scans | Runs at | Blocks? |
| --- | --- | --- | --- | --- |
| **Whole-state checks** | `docgraph [path]` | the current tree | pre-push / CI | **yes** — exit 1 on any finding |
| **`footgun-drift`** | `docgraph footgun-drift` | what a push *adds* | pre-push (advisory rider) | no — nags, always exit 0 |
| **`covers-drift`** | `docgraph covers-drift` | the code a push *changes* | pre-push (advisory rider) | no — nags, always exit 0 |
| **`doc-drift`** | `docgraph doc-drift` | the branch diff (incl. uncommitted) | agent Stop hook | **yes** — exit 2 on any finding |

Plus **read-only** helpers that never gate: `schema` (emits the frontmatter
vocabulary), and the doc-graph **views** `covers` / `index` / `stale` / `graph`
(`graph` serves both doc graphs — human render or JSON).

**Scope note.** The whole-state doc-graph checks audit *documents* (`docs/`,
`CLAUDE.md`, config-dir READMEs) — not content a site framework routes and
renders (an Astro/MkDocs content collection, a seed-data corpus); exclude those
per-repo via `.docgraphignore`. The `leaks` check is broader — it scans *all*
tracked files (see [`leaks`](#leaks--the-content-scan)).

## Whole-state checks

Seven checks run by default on `docgraph [path]`. **Everything is enforced; you
exclude explicitly** with `--skip <check[,check]>` — nothing is opt-in, so a
check added in a later version gates everywhere the day it lands, with no
run-list to update. (A nav-driven MkDocs repo, for instance, runs `--skip
orphans`.)

The checks span **two independent doc graphs**:

- **The content graph** — *findability*. Tracked docs joined by prose references:
  markdown `[x](y.md)` links ∪ bare/inline-code path mentions (`` `docs/x.md` ``),
  read over each doc's **body** (frontmatter stripped). `orphans` enforces it.
- **The metadata graph** — *structural placement*. Tracked docs joined by their
  frontmatter `links:` doc→doc edges (`part-of`, `see-also`, `depends-on`, …).
  `disconnected` enforces it.

They answer different questions and are **not** a single union: a doc can be
findable (reachable in the content graph) yet declare no place in the structure,
or vice versa. Both are flagged.

1. **Orphans** — a **content-graph island**: a non-root tracked doc with zero
   *inbound* content edges (nothing links to it or mentions its path). Roots
   (`CLAUDE.md`/`README.md`/`AGENTS.md`/`docs/index.md` + `--root`) are exempt.
   Frontmatter doc→doc edges do **not** count here — they belong to the metadata
   graph. Every real `.md` is audited, including docs outside `docs/` (e.g. a
   config-dir README); only agent tooling under `.claude/` and `.agents/` plus
   untracked scratch are excluded.
2. **Disconnected** — a **metadata-graph island**: a doc that carries frontmatter
   but participates in zero doc→doc edges in *either* direction. `covers` (code
   ownership) and `source` (external provenance) edges don't count — they point
   out of the doc→doc structure — so a doc whose only edges are `covers`→code is
   disconnected. Fix it with a real `part-of`/`see-also`/`depends-on` edge.
3. **Broken links** — a `[x](y.md)` whose target doesn't exist. Checked across
   the same tracked, non-ignored `.md` set as orphans.
4. **Untracked** — a `.md` on disk but not in git (a forgotten `git add`).
5. **Leaks** — tracked file *content* matching a configured leak pattern (see
   [`leaks`](#leaks--the-content-scan)).
6. **Frontmatter** — every doc except `README.md` (matched by basename) must
   carry a leading YAML block (first line exactly `---` to the next `---`) that is
   well-formed YAML and declares a `type`. A missing block, malformed YAML, or a
   missing `type` is a finding; `README.md` is exempt (see *Where to put
   frontmatter* below). `type` is an advisory vocabulary — see
   [`schema`](#docgraph-schema--the-frontmatter-vocabulary).
7. **Edges** — every internal `to` target in a frontmatter `links:` list (a
   repo-root-relative `.md` doc or code path) must exist, and `part-of` /
   `supersedes` edges between docs must not form a cycle. External URLs and
   `owner/repo:...` cross-repo targets are never checked.

### `docgraph schema` — the frontmatter vocabulary

```bash
docgraph schema    # prints the JSON Schema (draft 2020-12) to stdout
```

Emits the [JSON Schema](https://json-schema.org/) describing valid doc
frontmatter — the `type` / `verified` / `review` / `links` shape the
`frontmatter` and `edges` checks enforce, plus the advisory `type` / `rel`
vocabularies (as `x-docgraph-core-types` / `x-docgraph-core-rels`). Another tool
(an editor, a catalog builder) validates against the same rules docgraph uses
instead of re-encoding them. **Read-only** — never reads the repo, never part of
the gate.

**Where to put frontmatter.** Frontmatter is **required on every doc except
`README.md`** — the one basename-wide exemption. GitHub renders a leading YAML
frontmatter block as a metadata table above the page content, so a `README.md`
with frontmatter greets every visitor with a table of `type` and `links` rows.
Frontmatter is agent-facing metadata and the README is the human's front door —
so docgraph exempts `README.md` from the `frontmatter` check (and, having no
block, it is not a metadata-graph node, so `disconnected` never fires on it
either). A frontmattered README is still valid — it just renders that way, so
keep it out. docgraph itself follows this: `CLAUDE.md` carries a `covers` edge
onto its code plus the `part-of` tree, this README carries no frontmatter at all.

### `leaks` — the content scan

Scans **tracked file content** (working tree only, never git history) for
secret / owner-specific strings, to catch them before a repo goes public. Runs by
default; `--skip leaks` turns it off.

Scope is governed by **git tracking**, not the doc-graph ignore layers: every
`git ls-files` entry is scanned (so `.gitignore` decides what's excluded), and
`defaultIgnores` / `.docgraphignore` do **not** narrow it — only an explicit
`--ignore` glob does. A tracked `.claude/` config ships in a public clone, so it
stays in scope even though the doc-graph checks skip it.

Patterns come from a **global TOML file, never committed to a repo** — a per-repo
deny/allow list would itself enumerate your sensitive terms. Resolution:
`--leaks-config <path>` → `$DOCGRAPH_LEAKS` →
`$XDG_CONFIG_HOME/docgraph/leaks.toml` (default `~/.config/docgraph/leaks.toml`).

`terms` match literally and case-insensitively; `regex` / `allow_regex` are Go
regexps, also case-insensitive unless you opt out per-pattern with a leading
`(?-i)`. `allow` / `allow_regex` suppress a deny match they cover. `[[dir]]`
sections scope exceptions to files under an absolute `path` (a leading `~/` is
expanded; a non-absolute `path` is a fatal config error).

    terms       = ["acme-host", "you@example.com", "/Users/you"]
    regex       = ['10\.0\.0\.\d+']
    allow       = ["github.com/you"]
    allow_regex = ['com\.example\.[a-z]+']

    [[dir]]
    path   = "/abs/path/to/repo"
    ignore = ["vendor/*.json"]        # skip vendored specs
    [[dir]]
    path  = "/abs/path/to/repo/sub"
    allow = ["some-project"]          # legit in this subtree

Deny rules fall into **groups**. Top-level `terms` / `regex` are the implicit
`default` group; a `[[group]]` block is a named deny list. A `[[dir]].ignore`
glob silences the `default` group by default, but `ignore_groups` *replaces*
that default rather than adding to it — name `"default"` alongside another
group to keep both silenced — so a private repo can blanket-ignore its own
footprint vocabulary while staying scanned for terms that must not appear in
*any* repo:

    regex = ['(?-i)AKIA[0-9A-Z]{16}']   # default group: secret shapes

    [[group]]
    name  = "footprint"
    terms = ["acme-host", "/Users/you"]

    [[group]]
    name  = "client"
    terms = ["big-client"]

    [[dir]]
    path          = "/abs/path/to/private-repo"
    ignore        = ["**"]
    ignore_groups = ["footprint"]     # footprint is fine here; client + secrets stay live

`default` is reserved for the top-level rules — a `[[group]]` claiming it, a
group with no name, a duplicate name, or no terms/regex of its own, an
`ignore_groups` naming an undefined group, or an `ignore_groups` with no
`ignore` globs are all fatal config errors.
`allow` / `allow_regex` are not group-scoped: naming the string suppresses it
whatever group it belongs to, which is the per-repo escape hatch for a class the
repo legitimately owns.

**The config is the sole source of rules — no hidden built-ins.** Generic secret
shapes (PEM, AWS, GitHub, Slack) are just `regex` entries you add, with a leading
`(?-i)` to keep them case-sensitive:

    regex = [
      '(?-i)-----BEGIN [A-Z ]*PRIVATE KEY-----',
      '(?-i)AKIA[0-9A-Z]{16}',
      '(?-i)ghp_[A-Za-z0-9]{36}',
      '(?-i)xox[baprs]-[A-Za-z0-9-]{10,}',
    ]

Because the config is machine-local, CI and fresh clones (no file) have no rules
and the scan is a no-op there — it stays a **local** pre-push gate. Handling:

- **No config file** → no rules, scans nothing, prints a warning. **Not** fatal
  (a hard-fail would brick every CI push).
- **Malformed config** (bad TOML, an unknown key, bad regex, a non-absolute
  `[[dir]]` path, a `[[group]]` with no terms or regex) → exit 2, fail-closed.
  A broken config is a real bug, not the "not set up yet" case. `--skip leaks`
  bypasses config loading entirely, so it's the escape hatch if a config trips
  this path — fix the config rather than leaning on the skip, since it also
  turns off the check.

**Known gaps:** no git-history *detection* (use the manual leak-audit skill);
*scrubbing* a known leak from history is `docgraph leaks-rules` below. No
per-rule messages.

### `docgraph leaks-rules` — export rules for history scrubbing

The `leaks` check scans the current tree. To scrub a leak already in **git
history**, export the vocabulary as a
[`git-filter-repo`](https://github.com/newren/git-filter-repo) rules file and run
the rewrite separately:

```sh
docgraph leaks-rules > rules.txt          # non-destructive: reads only the config
git filter-repo --replace-text rules.txt  # destructive: rewrites history
```

It emits one `regex:` line per deny rule, including every `[[group]]`'s rules
(terms escaped and case-insensitive;
`regex` entries stay case-insensitive unless they carry `(?-i)`, normalized to a
plain case-sensitive pattern), using filter-repo's `***REMOVED***` replacement. A
stderr summary reports any `allow` / `allow_regex` / `[[dir]]` rules it
**dropped** — filter-repo rewrites by content across all paths and history, so
those exceptions can't apply. Emitted patterns target filter-repo's Python regex
engine, so RE2-only syntax may need manual review.

> The rewrite changes commit SHAs: force-push and have every collaborator
> re-clone. docgraph itself never reads or rewrites history — it only exports the
> rules.

## `footgun-drift` — the advisory pre-push rider

`footgun-drift` scans only what a push **adds** to tracked markdown, and it is
**advisory**: it prints a nag but exits 0 and never blocks. It flags a footgun
**declaration** (a line-leading `Footgun:` marker or a bolded mid-line footgun
lead — introducing one, not mentioning it; a cross-reference or a bare `##
Footguns` heading never counts) on any *added* line.

```bash
docgraph footgun-drift                       # reads git's pre-push ref lines from stdin
docgraph footgun-drift --range base..head    # explicit range, for manual use
```

With no `--range` it reads the ref lines git feeds a `pre-push` hook on stdin and
derives `remotesha..localsha` per ref (a new branch falls back to the merge-base
with the nearest integration branch). A declaration counts only if its line is in
that range's *added*-line set, read at the head revision — so a declaration
already on the remote is never re-flagged. File scope is every `.md` the diff
touches, including `.claude/` skill files that the doc-graph checks exclude.

It is a **nag, not a judge**: it reports every added declaration because it can't
rank whether a stated "why" is good — that's a judgment call it doesn't pretend
to make. On a finding it asks two questions — (1) is this a real footgun (a trap
you hit, a tempting-but-wrong approach, a re-litigated decision)? (2) is it at
the right doc level (invariant → `CLAUDE.md`, rationale → `docs/`, human prose →
`README`)? Fix or leave it in a follow-up; nothing was blocked.

`DOCGRAPH_FOOTGUN_OFF=1` silences it outright (for a repo that doesn't use the
`Footgun:` convention); `docgraph install-hook --no-footgun-drift` generates a
hook that never invokes it. There is no in-file suppression marker.

## `covers-drift` — the advisory pre-push rider

`covers-drift` scans the **code** a push changes and reports any doc that
declares a frontmatter `covers` edge onto it while the doc itself went
untouched. Like `footgun-drift` it is **advisory**: it prints a nag, exits 0, and
never blocks.

```bash
docgraph covers-drift                       # reads git's pre-push ref lines from stdin
docgraph covers-drift --range base..head    # explicit range, for manual use
```

It's the graph join [`doc-drift`](#docgraph-doc-drift--the-stop-hook-staleness-gate)
can't do: a rewritten function whose doc describes the old behaviour in prose
leaves no removed symbol and no changed literal to grep for, but a `covers` edge
already declares that doc the architecture of record for the code. What it can't
do is *judge* — it has no way to know whether the doc actually needed
reconciling, which is exactly why it never gates.

With no `--range` it reads the ref lines git feeds a `pre-push` hook on stdin and
derives `remotesha..localsha` per ref, deduping findings across refs. Docs are
`.md` only — the same `RepoDocs`/`trackedMD` set every whole-state check reads,
unlike `doc-drift`'s doc-grep which also matches `.mdx`; "code" is every other
tracked path except the prose formats `.txt`/`.rst`/`.adoc`/`.markdown`. An
`.mdx` file is therefore neither: a `covers` edge declared in one never fires.

**Editing the doc silences it** — a doc the change set touched never fires, so
there's nothing to suppress and no in-file marker exists. A repo with no `covers`
edges never sees it at all. To turn it off outright: `DOCGRAPH_COVERS_OFF=1`, or
`docgraph install-hook --no-covers-drift` to generate a hook that never invokes
it.

> **Already have a hook installed?** The `covers-drift` invocation is only
> written into `.githooks/pre-push` at hook-*generation* time, so a hook
> generated before this rider existed reads this section but never runs it.
> Regenerate with `docgraph install-hook --force` to pick it up.

## `docgraph doc-drift` — the Stop-hook staleness gate

Wire `doc-drift` into your agent harness's `Stop` hook (invoked directly, no
wrapper) so it runs at the end of every turn, catching tracked docs that still
describe code which just changed underneath them — before the agent hands control
back.

It scans a **working-tree-inclusive** range — base→worktree, covering committed
*and* uncommitted changes, because it fires before a commit necessarily exists.
On a trunk branch the base is `HEAD` (uncommitted-only); on a feature branch it's
the closest integration branch's merge-base (the whole branch so far). It flags
two mechanical staleness classes, and both **block** the turn from ending:

1. **Dangling reference** — a symbol whose *definition* was removed in the diff
   and survives nowhere else in tracked code, but a tracked doc still names it.
2. **Anchored value drift** — a constant whose numeric *value* changed while a
   tracked doc still names the constant **and** shows the old literal.

```bash
docgraph doc-drift                        # bare: resolves the base itself, applies the loop-guard
docgraph doc-drift --range base..head     # explicit range — bypasses the loop-guard
```

Bare invocation applies a **once-per-HEAD loop-guard**: after it nags for a given
`HEAD`, a repeat at the same `HEAD` is silent, so an agent that keeps ending its
turn without acting isn't nagged every Stop. The next commit moves `HEAD` and
re-arms it. This de-dupes the *nag*, it doesn't suppress the *finding*.

A finding in either class **blocks** — prints to **stderr**, exits **2**. Both
are mechanical facts, which is what earns them a hook that can stop a turn;
judgment calls belong in the advisory pre-push riders instead.
`DOC_DRIFT_OFF=1` disables the whole subcommand outright, for a repo that
doesn't use the anchored-symbol-and-value convention it relies on. There is no
other suppression surface (no `.docgraphignore`, no per-finding flag, no inline
marker): a flagged reference is a judgment call — reconcile the doc, or confirm
it's intentional framed history.

Scope limits worth knowing:

- **Docs are `.md`/`.mdx`; "code" is everything else** except the prose formats
  `.txt`/`.rst`/`.adoc`/`.markdown`, which are excluded from the code scan so
  def-shaped prose (a `class …` sentence in a `CHANGELOG.txt`) isn't read as a
  removed definition.
- **Anchored value drift is not proximity-checked** — it fires when a
  symbol-naming doc *also* contains the old literal anywhere in the file, so it
  can over-report if that number appears coincidentally.
- **`--range` evaluates against the current working tree**, so a dirty tree can
  change the verdict for the same range. Bare Stop-hook mode diffs base→worktree
  and is self-consistent.
- It only catches those two mechanical classes — a paraphrased value or a
  reversed decision with no anchored symbol needs a semantic doc sweep.

## `covers`, `index`, `stale`, `graph` — read-only views

Four subcommands that query the doc graph for a human or an agent tool, built on
the same parse the seven checks use. All are **read-only**: never write, never
gate (not in `checkNames`, not `--skip`-able, not run by the generated hook),
always exit `0` on success — `2` only on a usage/git error.

```bash
docgraph covers <path>               # docs that cover <path> (repo-root-relative)
docgraph index                       # generated markdown index of the doc graph
docgraph stale                       # docs whose verified date is past its threshold
docgraph stale --older-than 90       # override the default 180-day threshold
docgraph graph                       # both doc graphs as human-readable markdown
docgraph graph --json                # ...or as a stable JSON payload (machine seam)
```

- **`covers <path>`** — prints every tracked doc that documents `<path>` via a
  frontmatter `covers` edge, directly or by covering a parent directory
  (`covers: src/auth/` covers `src/auth/login.go`). `<path>` is
  **repo-root-relative** — frontmatter edges resolve against the repo root, unlike
  an inline markdown link. Prints nothing (exit `0`) if no doc covers it. The same
  `covers` edges feed the
  [`covers-drift`](#covers-drift--the-advisory-pre-push-rider) pre-push rider, so
  declaring one both answers this query and gets the doc surfaced when its code
  changes.
- **`index`** — prints a **generated** markdown index: every doc with
  frontmatter, grouped by `type` (core types in canonical order, then custom
  types alphabetically), each as `- [label](path) — description` (the tail is
  omitted when a doc has no `description`). The label is the doc's `title` if
  set, else its **body H1**, else its path — so a doc gets a readable entry
  without restating its own heading in frontmatter; set `title` only when the
  index label should differ from the H1. It's a view, not a hand-maintained
  page — redirect it into a tracked file (`docgraph index > docs/index.md`) and
  regenerate when the graph changes.
- **`stale [--older-than <days>]`** — prints every doc whose `verified` date is
  past its staleness threshold: `docs/old.md (verified 2026-01-01 — 195d old,
  threshold 180d)`. The threshold is `--older-than` (default **180**) unless the
  doc's own `review:` cadence (e.g. `review: 90d`) overrides it. A doc with no
  `verified` date, or an unparseable value, is silently skipped — malformed
  frontmatter is the `frontmatter` check's concern.
- **`graph [--json]`** — serves **both** doc graphs over the same node set the
  gate reads (so the served graph and the gated graph can never diverge). Default
  output is human-readable markdown: the `part-of` hierarchy, cross-reference
  edges, and both island lists (content-graph and metadata-graph). `--json` emits
  a stable, `schemaVersion`-stamped payload instead — the machine seam a catalog
  builder or an ecosystem-graph tool ingests. The JSON keys are
  camelCase throughout: `schemaVersion`, `repoRoot`, `nodes`, `contentEdges`,
  `metadataEdges`, `islands` (`{content, metadata}`). Edge/island slices are
  always present as arrays (empty, never `null`), so a consumer keys off their
  presence. `schemaVersion` is `1`; a breaking change to the shape bumps it rather
  than silently reshaping the payload. `--root`/`--ignore` apply as elsewhere.

  `graph` reads the working tree by default. Pass `--ref <ref>` (e.g. `--ref HEAD`,
  `--ref dev`) to build the graph from the **committed state at that ref**, read
  entirely from the git object store — so it also runs inside a **bare** repo (no
  checkout). On a clean checkout `--ref HEAD` matches the default output exactly; it
  diverges only by ignoring uncommitted/untracked changes to tracked docs, which is
  what "at a ref" means. This is the mode a scanner (e.g. Mycelium) uses to read a
  bare repo store without materializing a work-tree.

## Install

**Guided (Claude Code):** `/docgraph:install` — installs the binary, offers to
wire the `doc-drift` Stop hook into `~/.claude/settings.json`, offers this repo's
pre-push gate, and seeds the leaks config.

**Manual:**

```bash
curl -fsSL https://raw.githubusercontent.com/lockyc/docgraph/main/install.sh | bash
```

Runs `go install` (from the current checkout if you're in one, else `@latest`),
seeds `~/.config/docgraph/`, and prints where the binary landed. Or directly:

```bash
go install github.com/lockyc/docgraph/v3@latest   # or, from a checkout: just install
```

Needs **Go** (the install is `go install`) and **git** on PATH (docgraph shells
out to it at runtime). The module dependencies are `github.com/BurntSushi/toml`
(config decode) and `gopkg.in/yaml.v3` (frontmatter decode); the rest is the Go
stdlib.

### As a pre-push gate

```bash
docgraph install-hook [path]                    # gate: enforce ALL checks (default)
docgraph install-hook --skip orphans            # nav-driven repos (no orphan gate)
docgraph install-hook --ignore '**/*_test.go'   # bake an --ignore glob into the hook
docgraph install-hook --force                   # regenerate an existing hook
docgraph install-hook --no-footgun-drift        # omit the footgun-drift rider
docgraph install-hook --no-covers-drift         # omit the covers-drift rider
```

The generated hook runs the whole-state gate `docgraph .` (a bare invocation, so
a later-version check is enforced without regenerating), then the advisory
`docgraph footgun-drift` and `docgraph covers-drift`, each fed git's pre-push
stdin. Only the first can block a push — both riders ride with `|| true`, so not
even an operational error in one can abort it. It writes a tracked `.githooks/pre-push`
and sets `core.hooksPath` for this clone (others activate with `git config
core.hooksPath .githooks`). It refuses to clobber an existing hook (pass
`--force`, or integrate a docgraph call into your own). It fails **closed** — a
missing `docgraph` blocks the push, because a gate that skips when its tool is
absent is a false green.

## Usage

```bash
docgraph [path]                     # path defaults to '.'; enforces all checks
docgraph --root wiki/Home.md        # add an extra entry point (repeatable)
docgraph --ignore 'vendor/**'       # exclude a glob from checks (repeatable)
docgraph --skip orphans             # exclude a check (comma-separated)
docgraph --leaks-config <path>      # override the global leak rules file
docgraph --config <path>            # override the global config.toml (usage logging)
docgraph footgun-drift              # advisory: reads pre-push ref lines from stdin
docgraph covers-drift               # advisory: docs covering the code a push changes
docgraph doc-drift                  # Stop-hook: working-tree-inclusive diff
docgraph schema                     # print the frontmatter JSON Schema (read-only)
docgraph covers <path>              # read-only: docs that cover <path>
docgraph index                      # read-only: generated markdown index
docgraph stale                      # read-only: docs past their freshness threshold
docgraph graph [--json]             # read-only: both doc graphs (markdown or JSON)
docgraph version                    # print version (also --version, -v)
```

**Exit codes.** `docgraph [path]`: `0` clean · `1` findings · `2` usage / not a
git repo / malformed leak config. `footgun-drift` and `covers-drift` are
advisory: `0` regardless of findings (nag on stdout), `2` only on a git/usage
error. `doc-drift` blocks: `0` clean (or loop-guard-silenced) · `2` on a
dangling-reference or anchored-value finding (stderr) or on an error. `covers` /
`index` / `stale` / `graph` are read-only: `0` always on success, `2` only on error.

A clean run prints a single line (`docgraph: clean ✓ (N tracked .md, M reachable,
0 findings)`), so a green pre-push gate doesn't bury the terminal. Only on a
finding does the output turn self-describing — a banner, the sections that
actually have findings, and a footer explaining what docgraph is, why the
non-zero exit aborts the push, and how to remediate each category — so a failed
push doesn't have to be reverse-engineered.

> **v3 breaking changes:** (1) `orphans` is now the **content-graph island** rule
> (a non-root doc with zero inbound links/mentions) — frontmatter edges no longer
> make a doc "reachable". (2) A new `disconnected` check flags **metadata-graph
> islands** (a frontmatter doc with no doc→doc edge). (3) Frontmatter is now
> **required** on every doc except `README.md`. A model-A repo may see new
> `frontmatter`/`disconnected` findings on `@latest` — conform (add a `type:`
> block and a `part-of`/`see-also` edge) rather than `--skip`. The module path
> moved to `github.com/lockyc/docgraph/v3`; reinstall from the `/v3` path.
>
> **v2 breaking change:** the `--checks` (include) flag was removed. docgraph now
> enforces every check by default; use `--skip` to exclude one, and regenerate any
> installed hook with `docgraph install-hook --force`. A stray `--checks` prints a
> migration message and exits 2.

### Entry points (roots)

Reachability starts from whichever of `CLAUDE.md`, `README.md`, `AGENTS.md` (repo
root) and `docs/index.md` are tracked, plus any `--root` you add. This covers both
a whole doc repo and a project whose docs are `CLAUDE.md` + `docs/`.

### Ignoring paths

`**/superpowers/**`, `.claude/**`, and `.agents/**` are ignored by default for the
doc-graph checks (untracked scratch, and agent skill/config tooling that's never
part of the doc graph). Add more via `.docgraphignore` (gitignore syntax) or
repeatable `--ignore` globs (`**`, `*`, `?`). The leak scan honors only `--ignore`,
not the default/`.docgraphignore` layers — see [`leaks`](#leaks--the-content-scan).

**No inline markers.** Every suppression lives in config or on the command line —
`.docgraphignore`, `--ignore`, `--skip`, and the leaks config's `allow` /
`allow_regex`, and a `[[dir]]`'s `ignore` + `ignore_groups`. docgraph never reads a
suppression comment inside the audited files. `footgun-drift`, `covers-drift`
and `doc-drift` have no in-file escape at all — they're opted out only
whole-check, via `DOCGRAPH_FOOTGUN_OFF=1` /
`--no-footgun-drift`, `DOCGRAPH_COVERS_OFF=1` / `--no-covers-drift`, and
`DOC_DRIFT_OFF=1` respectively.

### Doc models and when to `--skip orphans`

The default is a **prose-linked, frontmattered** doc graph: entry docs link or
mention their way through `docs/` (satisfying `orphans`), and every doc carries a
`type:` block placed with a doc→doc edge (satisfying `frontmatter` and
`disconnected`). That fits most repos. Two exceptions:

- **Nav-driven MkDocs sites** — a `docs/` with no `nav:` block; MkDocs
  auto-builds the sidebar and pages never cross-link, so every page is a
  content-graph island by design, and such pages usually carry no `type:`
  frontmatter either. Gate with `--skip orphans` and, unless you'll add a `type:`
  block to every page, `--skip frontmatter` (which also moots `disconnected`,
  since a doc with no frontmatter isn't a metadata-graph node).
- Repos with genuinely unreferenced design docs report real orphans — link them
  from `CLAUDE.md`/`README`, or accept and `--skip orphans`.

**A content corpus — a cheatsheet section, a wiki's pages, a seed export,
verbatim clippings — asks one question: is it yours to conform?**

- **Hand-curated → conform it.** Give each page a `type:` (plus a `part-of` edge
  into an index doc so it isn't a metadata island) and the corpus one
  hand-maintained prose-link index page for content reachability. No
  `.docgraphignore`, no `--skip`. A foreign frontmatter vocabulary is no
  obstacle — unknown keys ride along untouched, so an Obsidian clipping keeps
  `created`/`source`/`author` and just gains `type: reference`. The corpus becomes
  a real graph node, and `broken`/`untracked` keep covering it.
- **Derived / never hand-edited → exclude it** with `.docgraphignore`, because a
  regen would drop any `type:` you added, so conforming can't stick.
- **`--skip` is wrong either way** — it's repo-wide, so a corpus's conventions
  also disable those checks on your `CLAUDE.md`/`README.md`, where they're valid.

A flood of orphans or frontmatter findings is a reason to look, not to exclude:
each has a cheap fix (an index page; a `type:` line), and excluding buys a quiet
gate by never checking that content again.

`.docgraphignore` does **not** exempt a corpus from `leaks` — that scan is scoped
by git tracking, not the doc-graph ignore layers. That's a separate decision with
a separate lever: a knowledge base legitimately full of the hosts, paths and
identifiers your rules match isn't leaking, it's just being itself, so silence it
in the **leaks config** with a `[[dir]]` `ignore` for that corpus. Two ignore
layers, two questions — "is this a doc graph?" and "should this content be leak
scanned?" — answer them independently.

A repo that doesn't use the `Footgun:` convention opts out of `footgun-drift`
outright (it's not a check, so no `--skip` name) — `DOCGRAPH_FOOTGUN_OFF=1` or
`install-hook --no-footgun-drift`. `covers-drift` has the same pair
(`DOCGRAPH_COVERS_OFF=1` / `install-hook --no-covers-drift`), though a repo with
no `covers` edges has nothing to turn off.

## Usage logging

docgraph can append one JSON line per run to a local log, for usage and finding
trends over time across all your repos. It is **opt-in** and machine-local: off
unless a global `config.toml` enables it, so CI, fresh clones, and contributors
never log.

Enable it in `~/.config/docgraph/config.toml` (resolved `--config` →
`$DOCGRAPH_CONFIG` → `$XDG_CONFIG_HOME/docgraph/config.toml`):

```toml
[log]
enabled = true
level   = 1                                   # 1 counts · 2 +paths · 3 +findings
# path  = "~/.local/state/docgraph/usage.jsonl"   # optional; this is the default
```

Records land in `$XDG_STATE_HOME/docgraph/usage.jsonl` (default
`~/.local/state/docgraph/usage.jsonl`), overridable via `[log].path` or
`DOCGRAPH_LOG`. A level-1 record:

```json
{"ts":"2026-07-09T21:30:00+10:00","version":"3.0.0","repo":"/abs/git/root",
 "cmd":"run","checks":["broken","disconnected","edges","frontmatter","leaks","orphans","untracked"],
 "exit":1,
 "counts":{"broken":1,"disconnected":0,"edges":0,"frontmatter":0,"leaks":0,"orphans":0,"untracked":0}}
```

**Detail level** trades richness for exposure:

- **1 — counts only.** No paths, no content. Safe default.
- **2 — adds `files`.** Flagged paths (broken/leaks include `file:line`), but
  **never leak match text.**
- **3 — adds `findings`.** Full detail **including leak match strings** — this
  turns the log into exactly the sensitive-string sink `leaks` exists to prevent.
  Use only on a trusted machine.

Notes:

- **Absent config → silently off** (the normal state). A **malformed**
  `config.toml` warns and disables logging but **does not fail the run** — logging
  is auxiliary, so a log-config typo must never block a push (unlike a malformed
  `leaks.toml`, which is fatal).
- `DOCGRAPH_NO_LOG=1` disables logging for one run even when the config enables it.
- Best-effort: an unwritable log file is silently skipped and never changes the
  exit code.

## Known gaps

Anchor validity (`y.md#missing`), external-URL liveness, raw `<a href>` in HTML
blocks, per-section `index.md` implicit-nav, and repo-specific conventions are out
of scope. Markdown-link extraction (broken-link detection, link edges) skips
fenced/inline code so example paths aren't treated as real links; the orphan
*reachability* pass, by contrast, does read inline-code path mentions.
`footgun-drift`'s declaration scan has no code-fence awareness — an example
`Footgun:` line inside a ` ``` ` block reads as a real declaration.

## Development

```bash
just test    # go test ./...
just build   # go build -o docgraph .
just install # go install . -> ~/go/bin/docgraph
just gate    # gofmt check + vet + tests (pre-release gate)
```

Work lands on the `dev` integration branch; `main` is the release branch and only
fast-forwards to a tagged release. Branch feature/fix work off `dev`, run `just
gate` before merging. Releases follow [semver](https://semver.org): the root
`VERSION` file is the single source of truth (embedded via `go:embed`), and `just
release` tags `v<VERSION>` and publishes the GitHub release. Consumers `go install
…@latest`, so a release moves everyone's pinned tool — keep `main` releasable.

The installed binary must be reachable when a hook fires: the generated pre-push
hook resolves docgraph via PATH **and** the Go bin dir (`$GOBIN` / `$GOPATH/bin` /
`~/go/bin`), because git runs hooks with the caller's PATH and GUI clients /
sandboxed agents often push with a bare PATH that omits `~/go/bin`.

Layout: `main.go` is a thin CLI (flags → audit → report → exit code);
`internal/audit/` holds the logic (link parsing, glob-ignore, git wrappers plus
diff helpers, the whole-state `Audit` orchestrator, the two graphs
`BuildContentGraph`/`BuildMetadataGraph` and the `BuildGraphView` seam behind the
`graph` view, the leak scanner, the diff-scoped `FootgunDrift` and `CoversDrift`,
and the `DocDrift` orchestrator that diffs code and greps docs). See `CLAUDE.md`
for design invariants.

---
type: architecture
links:
  - rel: covers
    to: internal/audit
    note: the invariants every check in this package must hold
  - rel: covers
    to: main.go
    note: CLI surface, exit contracts, hook wiring
---

# docgraph — notes for the next agent

A Go CLI (stdlib + `github.com/BurntSushi/toml` for config decode and
`gopkg.in/yaml.v3` for frontmatter decode; shells out to `git`) with four
independent audit modes, each with its own trigger, plus
read-only subcommands (`schema`, and the doc-graph views `covers`/`index`/
`stale`/`graph`):

- **`docgraph .`** — seven **whole-state** checks against the current tree,
  spanning **two doc graphs** (see the two-graphs footgun):
  `orphans` (a non-root tracked `.md` that is a **content-graph island** — zero
  inbound markdown-link/path-mention edges over the doc *body*), `disconnected`
  (a frontmatter doc that is a **metadata-graph island** — zero frontmatter
  doc→doc edges in *or* out), broken internal `.md` links, untracked `.md`, a
  `leaks` content scan, `frontmatter` (every doc except `README.md` must carry a
  leading YAML block that parses and declares a `type` — see "Frontmatter model"
  below), and `edges` (a frontmatter `links:` list's internal targets must exist,
  and `part-of`/`supersedes` edges must not cycle). All seven are **enforced by
  default**; exclude one with `--skip` (no opt-in — an opt-in check enforces
  nothing). A finding exits non-zero — that's the pre-push gate.
- **`docgraph footgun-drift`** — a **diff-scoped, advisory** pre-push subcommand:
  flags *every* footgun declaration added in the pushed range (no rationale
  judgment, never re-scans the existing corpus) and **exits 0** — a nag to
  double-check the declaration, never a block.
- **`docgraph covers-drift`** — a second **diff-scoped, advisory** pre-push
  subcommand, `footgun-drift`'s sibling: flags a doc that declares a frontmatter
  `covers` edge onto code the pushed range modified while the doc itself went
  untouched, and **exits 0** — a nag, never a block. It is the graph join
  `doc-drift` cannot do (a rewritten function whose doc describes the old
  behaviour in prose leaves no removed symbol and no changed literal to grep),
  but it judges nothing, so it must not gate. Editing the doc is the escape
  hatch; a repo with no `covers` edges never sees it.
- **`docgraph doc-drift`** — a **Stop-hook, blocking** subcommand: scans the
  branch's working-tree-inclusive diff (base→worktree, committed + uncommitted)
  for two mechanical staleness classes — a **dangling reference** (a symbol whose
  definition was removed but a tracked doc still names it) and **anchored value
  drift** (a constant whose numeric value changed while a doc still names the
  symbol and shows the old literal) — and **exits 2** to block the Stop. Both
  classes are mechanical facts, which is what earns it the Stop hook; it carries
  no advisory findings at all (see the Stop-hook dead-end footgun).
- **`docgraph schema`** — read-only, no repo state read at all: emits the JSON
  Schema (draft 2020-12) describing the frontmatter vocabulary that
  `frontmatter`/`edges` enforce, so another consumer (an editor, a catalog
  builder) conforms to it instead of re-encoding it. Never part of the gate.
- **`docgraph covers`/`index`/`stale`/`graph`** — read-only doc-graph **views**:
  they read the graph via `audit.RepoDocs`/`audit.BuildGraphView` (the same
  `parseDocs`/graph-build path the whole-state checks use, malformed docs simply
  omitted) and never gate — not `checkNames` entries, not `--skip`-able, never
  invoked by the generated pre-push hook, always exit `0` on success regardless of
  what they print. `covers <path>` answers "which doc governs this file" (a
  `covers` frontmatter edge, direct or parent-directory); `stale` reads the
  `verified`/`review` freshness fields the frontmatter model has carried since
  the `schema` vocabulary but that no check has read until now; `index` is a
  **generated** view (`IndexMarkdown`), not a hand-maintained page — redirect
  it into a tracked file rather than editing the output; `graph` renders **both**
  graphs (`BuildGraphView`) — a human markdown view (part-of tree + cross-refs +
  both island lists) by default, or a stable `schemaVersion`-stamped JSON payload
  with `--json`, the machine seam a catalog / Mycelium consumes.

`frontmatter`, `edges` and `disconnected` are ordinary `checkNames` entries —
whole-state, `--skip`-able exactly like orphans/broken/untracked/leaks.
`footgun-drift`, `covers-drift` and `doc-drift` are **not** `checkNames` — each is
its own subcommand with its own trigger (a git range / a Stop invocation), not a
`docgraph .` check. `schema`
and the `covers`/`index`/`stale`/`graph` views are a third kind again: not a check
and not diff-scoped, no trigger of their own — `schema` emits a fixed vocabulary,
the views query the current tree read-only. There's also an **opt-in usage
log** (see the logging footgun). Human-facing usage lives in `README.md`; this
file carries the invariants and footguns.

## Intended use

docgraph is built to run as a **pre-push documentation gate** (and in CI): the
six whole-state checks exit non-zero on a finding so a broken doc-graph blocks
the push without a wrapper. `docgraph install-hook` writes a tracked
`.githooks/pre-push` for that; the generated hook also runs `footgun-drift` and
`covers-drift` as **advisory** riders — each prints its nag but never blocks, and
each hook line ends `|| true` so not even an operational error can abort a push.
Separately, `docgraph doc-drift` is meant to be wired as a **Stop hook** by the
agent harness (e.g. a Claude Code `Stop` hook entry that runs `docgraph
doc-drift`): it fires at the end of a turn, not at push time, so a dangling
reference or stale anchored value is caught and blocked before the agent hands
control back — see
[`doc-drift`](README.md#docgraph-doc-drift--the-stop-hook-staleness-gate) in
`README.md`.
Only blocking findings belong there; the Stop-hook dead-end footgun explains why
an advisory check is a pre-push rider instead.

## What it is (and is not)

- **Agent-facing, not human-facing.** It measures the graph an agent traverses
  (grep + `[x](y.md)`), *not* whether a human can reach a page.
- **Reads two doc graphs, frontmatter, and, for `leaks`, arbitrary file
  content — all as whole-state.** `orphans` reads the **content graph** (prose
  findability: markdown links ∪ path-mentions over each doc's body);
  `disconnected` reads the **metadata graph** (frontmatter doc→doc edges);
  `broken`/`edges` check link/edge target existence (`edges` additionally checks
  `part-of`/`supersedes` cycles among frontmatter edges); `frontmatter` requires
  a well-formed, `type`-bearing leading YAML block on every doc except
  `README.md`; `leaks` scans tracked file *content* (code included) for
  configured leak patterns. All read the current tree, never git history.
- **`footgun-drift`, `covers-drift` and `doc-drift` read a *diff*, not repo
  state (the diff-scoped-subcommands invariant).** `footgun-drift` scans added
  markdown lines (like `leaks`, but diff-scoped); `doc-drift` diffs tracked
  **code** (removed definitions, changed constants) and greps the *docs* for stale
  references to what changed — the one check that compares two different file
  types; `covers-drift` joins the same code diff against the graph instead of a
  grep — `changedCode` against the `covers` edges of the parsed doc set. They
  share this check-what-changed model but stay **separate** subcommands because
  their trigger and diff source differ: `doc-drift` a Stop hook over the
  working-tree code diff, `footgun-drift` a pre-push subcommand over the pushed
  range's markdown, `covers-drift` a pre-push subcommand over that same range's
  code side. The six `docgraph .` checks have no range concept — reachability,
  link existence and leak content are properties of the current tree, not a range.

## Frontmatter model

A doc's optional leading YAML block (`SplitFrontmatter`: present only when the
file's first line is exactly `---`, ending at the next line that's exactly
`---`) is the agent-facing metadata layer `frontmatter`/`edges` and `docgraph
schema` all key off:

- **`type`** — required whenever a block is present (missing it is a
  `frontmatter` finding; malformed YAML is a separate `frontmatter` finding
  regardless of `type`). Core vocabulary (`CoreTypes` in
  `internal/audit/frontmatter.go`): `runbook`, `architecture`, `reference`,
  `decision`, `guide`, `index` — advisory, not enforced; a custom value is
  accepted (tolerate-unknown), never rejected.
- **`verified`/`review`** — freshness metadata: `verified` is the date the doc
  was last checked against reality, `review` is a per-doc staleness-cadence
  override (e.g. `90d`). Read by the `stale` **view** only — no *check* reads
  either field, so neither can ever gate a push.
- **`links`** — typed edges (`Edge{Rel, To, Note}`, the "label the link"
  model). `rel` core vocabulary (`CoreRels`): `covers`, `part-of`,
  `supersedes`, `depends-on`, `runbook-for`, `see-also`, `source` — advisory,
  custom allowed. `to` is **repo-root-relative** (`ResolveEdgeTarget` resolves
  against the repo root, not the source doc's directory — unlike a markdown
  link) with its kind inferred (`ClassifyTarget`), never declared: an internal
  `.md` path is a doc edge (existence-checked; as of v3 a frontmatter doc-edge
  no longer feeds content-graph reachability, it's a metadata-graph edge in
  its own right — see the two-graphs footgun below), another internal path is
  a code edge (existence-checked only), a URL/`mailto:` is external
  (unverifiable, never checked), and an `owner/repo:path` form is cross-repo
  (deferred to Mycelium — docgraph sees only one repo — and never a finding
  here).
- **Cycles**: only `part-of`/`supersedes` edges between tracked docs form the
  acyclic-checked graph (`detectCycles`/`cycleRels`); every other `rel` is a
  cross-reference, not hierarchy/lineage, and is exempt from cycle detection.

`CoreTypes`/`CoreRels` are single-sourced in `internal/audit/frontmatter.go`;
`docgraph schema` reads them into the emitted JSON Schema rather than
restating the vocabulary, so the schema and the checks can't drift apart.

## Footguns

- **Measures prose-link reachability on purpose — NOT MkDocs nav.** A MkDocs
  site with no `nav:` block auto-builds its sidebar from the file tree, so every
  page is trivially reachable *for a human*. That is not what this tool checks:
  an agent doesn't read the sidebar. Do **not** "fix" orphan detection to defer
  to MkDocs nav — it would make the tool always report zero orphans and destroy
  its purpose.
- **`footgun-drift` is diff-scoped ON PURPOSE — do NOT re-add `footguns` to
  `checkNames`.** A whole-state footgun check re-scanning every tracked `.md`
  floods on the existing corpus: every already-accepted note re-flags on every
  unrelated push (worse now it flags *every* declaration, not just un-rationalized
  ones). Scoped to what's *new* — declarations added in the pushed range — it
  stays quiet on content already on the remote. It shares the check-what-changed
  model with `covers-drift`/`doc-drift` but stays a separate subcommand (the
  diff-scoped-subcommands invariant above; see the Stop-hook footgun for why
  `covers-drift` cannot fold into `doc-drift` despite the shared diff).
- **`leaks` rules live in a GLOBAL file, never in the repo — on purpose.** A
  per-repo deny list committed to a public repo *is itself the leak* (it
  enumerates the owner's sensitive terms), and the footprint vocabulary is
  identical across repos. **The leaks config is the single control surface** — no
  per-repo/inline override (referenced across the footguns below). The README's
  `leaks` section documents the TOML schema, the
  `--leaks-config`→`$DOCGRAPH_LEAKS`→`$XDG_CONFIG_HOME` resolution (XDG, never
  `os.UserConfigDir()` — wrong on macOS for a CLI), and the `leaks-rules` export.
  The load-bearing invariants:
  - **The config is the SOLE source of rules — NO hardcoded built-ins.** Generic
    secret shapes (PEM/AWS/GitHub/Slack) are `regex` entries the owner adds; the
    binary ships none. Rules hidden in a binary can't be seen or tuned — the config
    being the single visible source is the point. Do NOT reintroduce built-ins.
  - **Absent config is NOT fatal; malformed IS.** leaks runs by default (incl. CI,
    which has no machine-local file), so an absent file → no rules, no-op, warn; a
    malformed config → fatal exit 2, from either of two sites: `LeakConfig.compile`
    (`internal/audit/leaks.go`) for bad regex/dir/group shapes, and `loadLeakConfig`
    (`main.go`) for bad TOML and an unknown key.
    Do NOT restore hard-fail-on-absent — it would brick every CI/fresh-clone push.
  - **An `ignore` glob suppresses only the groups it names — do NOT restore
    whole-file skipping.** Top-level `terms`/`regex` are the `default` group; a
    `[[group]]` block is a named deny list, and `[[dir]].ignore_groups` (absent =
    `["default"]`) decides what an ignore glob silences. The blanket
    `ignore = ["**"]` a private repo needs for its own footprint vocabulary must
    not blind the scan to terms that may not appear in ANY repo — that conflation
    is the bug groups exist to fix. A repo that legitimately owns a class names
    the class in `ignore_groups`, or its individual strings in `allow`; there is
    still no per-file marker. `ReplaceTextRules` exports every group's rules, so a
    history scrub covers them too.
  - **`leaks-rules` targets filter-repo's Python `re`, not Go/RE2.** A leading
    `(?-i)` is normalized to a plain case-sensitive rule (Python `re` rejects the
    bare flag-clear Go/RE2 accepts, which would abort the rewrite); other RE2-only
    syntax needs manual review first. `allow`/`allow_regex`/`[[dir]]` are dropped
    with a warning (filter-repo rewrites by content across all history, so span/path
    exceptions can't apply). docgraph never reads or rewrites history itself — the
    rewrite is a separate external `git filter-repo` step; history *detection* stays
    out of scope (the `pre-public-leak-audit` skill).
- **Enforce-by-default, exclude explicitly — never an opt-in/include model.**
  Every check runs by default; `--skip <check[,check]>` is the only way to not run
  one. The removed `--checks` (include-list) flag could not enforce: a check added
  later is silently absent from every existing `--checks` list, so it enforces
  nowhere until each repo edits its list. With the exclude model a new check is
  enforced everywhere the day it lands, and the generated hook runs a bare
  `docgraph .` for the same reason. `run` and `install-hook` reject a stray
  `--checks` with a migration message (exit 2). Do NOT reintroduce an include-list
  default.
- **`leaks` scope is git tracking, not the doc-graph ignore layers.** `LeakScan`
  scans every file `git ls-files` returns (`.gitignore` governs scope), honoring
  only `--ignore` CLI globs — **not** `defaultIgnores`/`.docgraphignore`. A tracked
  file ships publicly regardless of doc-graph scope, so a tracked `.claude/` config
  (excluded from orphans/broken/untracked as non-documentation) is exactly where
  owner-specific strings hide and must stay in leak scope.
- **`[[dir]]` exclusions are keyed by ABSOLUTE path and local-only** — they take
  effect only where the global config lives, so CI/fresh clones (no config → no
  rules → no-op) never apply them (`expandDirPath` rejects a non-absolute path,
  `internal/audit/leaks.go`). A repo whose own tracked fixtures trip its owner's
  rules is silenced *in the config* with a `[[dir]] ignore` (docgraph's own entry
  ignores `**/*_test.go`) — the leaks config is the single control surface, not a
  per-repo `--skip`/`--ignore` or an inline marker (see next).
- **No inline suppression markers — every control is config or CLI.** docgraph
  never parses a suppression comment/pragma out of the files it audits.
  Suppression is *only* `.docgraphignore`/`--ignore`/`--skip` (doc-graph scope)
  and the leaks config's `allow`/`allow_regex`, and a `[[dir]]`'s `ignore` +
  `ignore_groups` (leak scope);
  `footgun-drift`/`covers-drift`/`doc-drift` have no in-file escape, opted out only
  whole-check via `DOCGRAPH_FOOTGUN_OFF=1`/`--no-footgun-drift`,
  `DOCGRAPH_COVERS_OFF=1`/`--no-covers-drift`, and `DOC_DRIFT_OFF=1`. Deliberate: an
  inline marker committed to a public repo is itself a visible "here be a secret"
  annotation (same reason the leaks deny-list stays out of the repo), and a
  per-file override is what the single-control-surface model exists to avoid — a
  comment scanner would also have to read file content to honor self-referential
  annotations, silently un-nagging whatever it's placed on. A flagged
  `footgun-drift`/`doc-drift` reference is a judgment call — reconcile the doc or
  confirm it's intentional framed history — de-duped only by doc-drift's
  once-per-HEAD loop-guard; `covers-drift` needs no suppression surface (it fires
  only on a doc the change set left untouched, so editing that doc *is* the escape).
- **Code-block links are skipped deliberately.** `extractLinks`
  (`internal/audit/links.go`) ignores fenced and inline code so template/example
  paths don't register as real *links*. Asymmetry: the orphan **reachability**
  pass, `mentionsPath`, *does* read inline-code path mentions — that's how an agent
  follows a bare `` `docs/x.md` `` reference. Link-extraction and reachability
  answer different questions; don't unify them.
- **Two graphs, not one union — do NOT re-merge them.** The **content graph**
  (markdown links ∪ path-mentions over doc bodies) enforces *findability* via an
  inbound-only island rule (a non-root doc with zero inbound content edges is an
  `orphans` finding); the **metadata graph** (frontmatter doc→doc edges) enforces
  *structural placement* via an either-direction island rule (a frontmatter doc
  with zero doc→doc edges is a `disconnected` finding). They answer different
  questions (findability is inbound-only; placement is either-direction) and
  root-reachability is deliberately not enforced — the generated `graph` view
  provides findability, not a hand-curated index chain.
  - **`covers`/`source` edges don't count toward metadata placement** —
    `isMetadataEdge` (`internal/audit/graph.go`) excludes them even when the target
    is a tracked `.md`, since they point *out* of the doc→doc structure. So a doc
    whose *only* frontmatter edges are `covers`→code (this very file) is a metadata
    island that trips `disconnected`; the fix is a real doc→doc edge (a `part-of`
    up to the entry doc, or a `see-also` across), not demoting the `covers` edge.
- **Exclude tooling, not real docs — don't re-narrow to `docs/`.** Orphan
  candidates are *all* tracked `.md` except the `defaultIgnores` (`.claude/**`
  and `.agents/**` agent skill/config files, which aren't documentation, plus
  untracked scratch). A real doc outside `docs/` (a config-dir README, e.g. a
  `monitoring/README.md`) **is** a document and must be audited. `.claude/**` and
  `.agents/**` files are runtime tooling; a config-dir README is not — keep that
  distinction.
- **Usage logging is OPT-IN, side-channel, and MUST NOT alter the gate.** One JSONL
  record per run under `$XDG_STATE_HOME/docgraph/usage.jsonl` (XDG *state*, not
  config), only when a global `config.toml` `[log]` table opts in (the README's
  Usage-logging section has the config + level table). Load-bearing invariants:
  - **Separate file from `leaks.toml`.** `leaks.toml` is a dedicated rules file that
    may be synced on its own; `config.toml` holds `[log]`. Don't merge them.
  - **Malformed `config.toml` is NON-fatal here** — warn, disable logging, run
    continues. This deliberately DIVERGES from malformed-`leaks.toml`-is-fatal:
    leaks is an enforced protection, logging is auxiliary, so a log-config typo must
    never block a push. Absent config → silently off (no warning; the normal
    CI/clone/fresh state). Do NOT make either fatal.
  - **Level gates leak exposure.** L1 counts only, L2 adds paths, L3 adds full
    findings **including leak match text** — levels 1–2 must NEVER write a leak
    `Match` (`BuildRecord`, `internal/audit/usage.go`); only L3 (a documented,
    trusted-machine opt-in) does.
  - **Best-effort, never fails the run.** `maybeLog` swallows every error; the exit
    code is decided by findings alone. `DOCGRAPH_NO_LOG=1` is the one-off kill switch
    (mirrors `DOC_DRIFT_OFF`).
  - **`cmd` is a seam, not decoration.** Each record carries `"cmd":"run"`. It exists
    so a future `docgraph drift` subcommand logs through the *same* file with the
    *same* record shape — trends span both. Keep the field when adding a subcommand.
- **`footgun-drift` is a nag, not a judge — so it flags EVERY added
  declaration.** It detects a footgun *declaration* (a line-leading `Footgun:` or a
  bolded mid-line footgun lead, not a cross-reference or bare container heading)
  and reports it, doing **no** rationale detection
  (`scanDeclarations`/`isFootgunDeclaration`, `internal/audit/footguns.go`): a
  deterministic scanner can't rank whether a stated "why" is real — that would just
  reward typing "because". It prints the two-question test (is this a real footgun;
  is it at the right doc level — the `doc-and-audit-rigor` skill's test) and leaves
  the judgment to the pusher. Judging nothing, it does not block (the
  advisory-can't-block invariant, Stop-hook footgun below). Do NOT reintroduce
  rationale detection to "reduce noise" — an honest nag beats a fake judge.
- **`footgun-drift`'s file scope is `git diff --name-only <range> -- '*.md'`** —
  not the doc-graph ignore layers, and not the leaks git-tracking scope. Any `.md`
  the diff touches is in scope, including a `.claude/**` skill file that
  `orphans`/`broken`/`untracked` exclude as non-documentation — a footgun
  declaration added inside agent tooling is just as undocumented as one in
  `CLAUDE.md`. Do not apply `defaultIgnores`/`.docgraphignore` here.
- **An advisory check can NEVER be a Stop hook — that's why `covers-drift` is
  pre-push (the advisory-can't-block invariant).** A Stop hook has no channel that
  is both advisory and agent-visible: exit-0 stdout reaches only the debug log (the
  agent never sees it); exit-2 stderr, `decision:"block"`/`reason`, and
  `hookSpecificOutput.additionalContext` all reach the agent but all stop the turn
  from ending. That is structural — a Stop hook exists to decide whether Claude
  stops — so the tempting "fold `covers-drift` into `doc-drift`, they share a diff
  source" refactor cannot work: at Stop time the only way to be heard is to block,
  and `covers-drift` judges nothing so it must not. `doc-drift` blocks because its
  findings are mechanical facts; that is what earns it the Stop hook.

## Doc models (why `--skip` exists)

Repos fall into models the orphan check treats differently. The v3 default is
**stronger than a link check**: every doc except `README.md` must carry a
frontmatter block (`frontmatter`), and a frontmatter doc must sit in the
metadata graph (`disconnected`) — so a model that can't meet those needs a
wider `--skip`, not just `--skip orphans`.
- **A — prose-linked**: entry docs link/mention through `docs/`, and each doc
  carries a `type:` block placed with a `part-of`/`see-also` edge. Orphans and
  metadata islands are real. Enforce every check (the default) — docgraph's own
  repo is model A.
- **B — nav-driven MkDocs**: `docs/` with no `nav:` block; MkDocs auto-builds
  the sidebar, pages never cross-link → every page is a content-graph island
  *by design*, and MkDocs pages typically carry no `type:` frontmatter either.
  Run with `--skip orphans` and, unless you're willing to add a `type:` block to
  every page, `--skip frontmatter` (which also moots `disconnected`, since a doc
  with no frontmatter is not a metadata-graph node).
- **C — flat reference `docs/`**: design notes referenced by path. `mentionsPath`
  makes these content-reachable; genuine islands that remain are real gaps worth
  linking. Still owes each doc a frontmatter block + a placement edge like A.
- **D — a content corpus** (a cheatsheet section, a wiki's pages, seed data,
  verbatim third-party clippings): tracked `.md` the repo *publishes* or *feeds to
  something* rather than documents itself with. The deciding question is **not**
  "is this a knowledge base?" — it's **"is this corpus mine to conform?"**
  - **Hand-curated → CONFORM it, don't exclude it.** Give each page a `type:`
    (and, under v3, a `part-of` edge into an index doc so it isn't a metadata
    island) and give the corpus a hand-maintained prose-link **index page** for
    content reachability. That's it — no `.docgraphignore`, no `--skip`. A foreign
    vocabulary is not an obstacle: `Doc.Extra` (`yaml:",inline"`) exists so domain
    keys ride along, so an Obsidian clipping keeps `created`/`source`/`author` and
    merely gains `type: reference`. Reference instance: homelab's
    `docs/cheatsheet/**` (22 clippings folded in 2026-07-17). The corpus becomes a
    first-class graph citizen instead of a blind spot, which is the better outcome:
    excluding it means `broken`/`untracked` stop covering it too.
  - **Derived / never-hand-edited → exclude** (`.docgraphignore`). Conforming is
    not available: the generator would drop any `type:` you added on the next
    regen, so the edit can't survive. Reference instance: a private client app's
    `database/*-seed/**` (a regenerated export read as source material).
  - **`--skip` is wrong for both** — it's **repo-wide**, so a corpus's conventions
    silently disable those checks on `CLAUDE.md`/`README.md` too, where they're
    valid and wanted. Prefer conform; fall back to the path-scoped ignore.
  - **Footgun — "it floods orphans and frontmatter" is a reason to look, not to
    exclude.** Both floods have a cheap fix (an index page; a `type:` line), and
    reaching for the ignore because the numbers are big buys a quiet gate at the
    cost of never checking that content again. docgraph itself made this mistake
    in this section's first draft.

  **Leaks is a separate layer, and `.docgraphignore` never touches it** —
  `LeakScan` is scoped by git tracking, not the doc-graph ignore layers. A corpus
  legitimately full of the hosts, paths and identifiers the rules match is not
  leaking; that decision belongs in the **leaks config** as a `[[dir]]` `ignore`
  (its single control surface — not a per-repo `--skip`/`--ignore`, per the
  dir-scoped-exclusions footgun above). Two layers, two questions; don't conflate
  them. Scope caveat: a git-*untracked* corpus is already outside `leaks`
  entirely, since `LeakScan` walks `git ls-files`.

A repo that doesn't use the `Footgun:` note convention at all opts out of
`footgun-drift` entirely rather than passing `--skip` (it isn't a `docgraph .`
check to skip): set `DOCGRAPH_FOOTGUN_OFF=1`, or generate the hook with
`install-hook --no-footgun-drift` so it's never invoked in the first place.
`covers-drift` has the same pair — `DOCGRAPH_COVERS_OFF=1` or `install-hook
--no-covers-drift` — though a repo with no `covers` edges needs neither: with
nothing to join against, the check is already silent.
Likewise, a repo that doesn't use the anchored-symbol-and-value convention
`doc-drift` relies on (a doc naming a code symbol, a constant it also shows the
literal value of) disables `doc-drift` outright with `DOC_DRIFT_OFF=1` — it
isn't a `docgraph .` check either, so there's no `--skip` name for it.

## Roots

Auto = tracked ones of `{CLAUDE.md, README.md, AGENTS.md, docs/index.md}` +
`--root` additions. Unifies "whole doc repo" and "project with CLAUDE.md +
docs/" with zero config.

## Layout & commands

- `main.go` — thin CLI: flags, `run(args, stdout, stderr) int` (the seven
  whole-state checks), `runFootgunDrift(args, stdout, stderr) int` and
  `runCoversDrift(args, stdin, stdout, stderr) int` (the two diff-scoped
  advisory pre-push subcommands — `runCoversDrift` checks `DOCGRAPH_COVERS_OFF`,
  resolves ranges from `--range` or `rangesFromPrePushStdin`, calls
  `audit.RepoDocs` + `audit.CoversDrift`, prints via `printCoversDrift`
  (rendering `coversDriftMessage`) to **stdout** and returns 0 on a finding;
  exit 2 is reserved for a usage/git error, which is a tool bug rather than a
  finding), `runDocDrift(args, stdin, stdout, stderr) int` (the Stop-hook
  subcommand — checks `DOC_DRIFT_OFF`, resolves the diff spec via
  `docDriftDiffBase`, calls `audit.DocDrift`, and on a finding prints via
  `printDocDrift` to stderr and returns 2, gated on bare invocation by
  `docDriftGuardOK`'s once-per-HEAD marker under `docDriftStateDir()`),
  `runCovers`/`runIndex`/`runStale`/`runGraph` (the read-only views — each
  resolves the repo root, calls `audit.RepoDocs` (or `audit.BuildGraphView` for
  `runGraph`, which also takes `--json`), and prints; always `return 0`), report
  format, `maybeLog` (opt-in usage logging side-channel).
- `internal/audit/views.go` — `RepoDocs` (the shared parse path behind the
  views), `CoversOf`, `IndexMarkdown`, `StaleDocs` + `parseReviewDays`.
- `internal/audit/graph.go` — the two graphs: `BuildContentGraph`/`ContentGraph`
  (links ∪ path-mentions over doc bodies; `Islands()` = zero-inbound non-roots →
  `orphans`) and `BuildMetadataGraph`/`MetadataGraph` (`isMetadataEdge` doc→doc
  frontmatter edges; `Islands()` = zero-degree frontmatter docs → `disconnected`).
- `internal/audit/graphview.go` — `BuildGraphView`/`GraphView` +
  `GraphSchemaVersion` (the `graph` view's payload: reuses both `Build*Graph`
  fns, renders `Markdown()` or `JSON()`; the stable Mycelium seam).
- **`graph --ref <ref>` reads the doc-graph from the object store at a ref
  (bare-repo capable).** It builds via `BuildGraphViewAtRef` over a `refSource`
  (`git ls-tree` + `git show`/`fileAtRev`), reusing the *same* `buildContentGraph`/
  `parseDocsFrom`/`buildGraphViewFrom` internals as the working-tree path (a
  `worktreeSource`) — one graph computation, two sources, so the served graph can
  never diverge by mode. `--ref` mode must **not** call `GitRoot`
  (`rev-parse --show-toplevel` fails on a bare repo). Output shape is unchanged
  (`GraphSchemaVersion` stays 1); a `--ref HEAD` run on a clean tree equals the
  default run. Only `graph` is ref-aware — the audit checks stay working-tree only.
- `internal/audit/` — `links.go` (parse/resolve), `ignore.go` (`**` globs),
  `git.go` (`ls-files` wrappers **plus** the diff helpers `changedMarkdown`/
  `changedCode`/`addedLines`/`fileAtRev`/`ClosestBase` that `footgun_drift.go`,
  `doc_drift.go` and `covers_drift.go` use to read a range instead of a tree
  snapshot; `changedCode` shares `nonCodePathspec` with `gitDiff`/
  `stillDefinedInCode` so every code-side scan agrees on what "code" is),
  `leaks.go`
  (TOML config decode + dir-scoped content scan), `footguns.go` (the
  declaration scanner — `scanDeclarations`/`isFootgunDeclaration`; a
  *declaration* is a footgun being introduced, not a passing mention of one,
  and every one is reported — `scanDeclarations` does no rationale filtering),
  `footgun_drift.go` (`FootgunDrift`: runs `scanDeclarations` per range,
  keeps only declarations whose line is in that range's added-line set,
  dedupes by file:line), `doc_drift.go` (`DocDrift` + helpers
  `looksLikeSymbol`, `removedNotReadded`, `changedConstants`, `gitDiff`,
  `stillDefinedInCode`, `docGrepSymbol`, `docGrepValue`: diffs `gitDiff(root,
  spec)`, finds removed-and-not-readded definitions and changed numeric
  constants, then greps tracked docs for a lingering reference to either),
  `covers_drift.go` (`CoversDrift` + `CoversFinding{Doc, Paths}`: joins
  `changedCode` against the `covers` edges of an already-parsed doc set —
  `RepoDocs` — via `CoversOf`, dropping any doc `changedMarkdown` says the
  change set also touched, and deduping across ranges),
  `audit.go` (`Audit` → `Report`, the whole-state orchestrator — builds both
  graphs via `graph.go` and sets `Report.Orphans`/`Report.Disconnected` from
  their `Islands()`; `parseDocs` also enforces the frontmatter-required-except-
  README rule; unrelated to `FootgunDrift`/`DocDrift`/`CoversDrift`), `usage.go`
  (usage-log config + tiered `BuildRecord` + best-effort `LogRun`).
- `just test` / `just build` / `just install`. Tests build throwaway git repos
  in temp dirs, so `git` must be on PATH.
- **Install with `just install`** (or `go install .`) → `~/go/bin`. The binary is not
  reinstalled automatically, because `go install` only runs when invoked — so
  reinstall after changing the CLI or the local binary runs stale logic.
- `install.sh` + `.claude/commands/docgraph/install.md` (`/docgraph:install`) — the
  guided installer (`project-standards` item 13). `install.sh` is the curl-pipeable
  mechanism (`go install`, dual-mode IN_REPO/`@latest`, seeds `~/.config/docgraph/`,
  edits no global config); `/docgraph:install` is the guided layer that wires the
  `doc-drift` Stop hook into `~/.claude/settings.json`, offers the per-repo pre-push
  gate, seeds the leaks config, and installs the skill below. Neither depends on `just`,
  and both declare the module path once, in a `MODULE` var, rather than inline — so the
  `MODULE` lines are `/vN` sites a major bump must move along with every other
  `@latest` site (see the major-bump footgun). `/docgraph:install`'s copy is the one
  that already rotted: it inlined the *bare* path, which matches no `go.mod` at major
  ≥2, so the detect step reported `NOT_IN_REPO` from inside the checkout — and this
  sentence claiming otherwise is what kept the `/v2` sweep from looking there.
- `.claude/skills/docgraph/SKILL.md` — **the only thing that advertises the read-only
  views.** The gates push themselves at an agent (a hook fires whether or not it knows
  docgraph exists); `covers`/`index`/`stale` are pull-only, so an agent that never
  learns of them greps instead and the views may as well not exist. The skill's
  *description* line is the advertisement — permanently in context for the cost of one
  line, body loaded only when a doc question actually forms. The repo copy is the
  source of truth; `/docgraph:install` installs it to `~/.claude/skills/docgraph/`.
  Keep it honest about empty results: a repo with no `covers:` edges legitimately
  answers nothing, and an agent must not read that as "no doc exists".

## Branching & releases

- **`main` + `dev`.** `dev` is the integration trunk; `main` is the release branch —
  it only fast-forwards to a tagged release commit and stays a clean ancestor of `dev`.
  Never commit directly to `main` (drift breaks the fast-forward; fix by back-merging
  `main` into `dev`, never force-push). Feature/fix branches off `dev`.
- **Semver, `v`-prefixed tags.** The tracked root **`VERSION`** file is the single source
  of truth, `go:embed`-ed via `version.go` so `docgraph version` (also `--version`, `-v`)
  self-reports — never restate the version elsewhere. Consumers `go install …@latest`, so
  a release moves everyone's pinned tool: keep `main` releasable and bump major for a
  breaking CLI change, minor for a new feature, patch for a fix.
- **Cut a release** from `dev` with `VERSION` bumped + committed: `just release` runs
  `gate`, fast-forwards `main`, tags `v<VERSION>`, and publishes the GitHub release.
  **Notes are hand-written and required** — put them in `RELEASE_NOTES.md` (gitignored,
  per-release scratch) or pass a path: `just release <file>`. The recipe refuses to run
  without them, before it touches anything public. `gh --generate-notes` is deliberately
  *not* used: it summarises merged PRs, and this repo integrates on the trunk, so it only
  ever produced a bare changelog link.
- **The major version lives in the module path too** — `go.mod` currently declares
  `github.com/lockyc/docgraph/v3` (`VERSION` is `3.0.0`), and the `/vN` suffix must be
  bumped in lockstep with a major `VERSION` bump (`v4` → `module …/v4`). The `/vN`
  sites, all of which must move together: `go.mod`'s `module` line, the `internal/`
  import paths, and **every `go install …@latest` site** — the README install line,
  `install.sh`'s `MODULE` var, `/docgraph:install`'s `MODULE` var, `SKILL.md`, and the
  fail-closed hook's own "install it:" message that `hookScript` prints. This is Go's
  semantic import versioning, not a style choice; the footgun below is what enforcing
  it costs when you don't.

## Footgun — a major bump must move the module path, or the release is invisible

Go **ignores every `vN` tag (N≥2) on a module whose path lacks the matching `/vN`
suffix** — the proxy rejects the tag (`invalid version: module path must match major
version`) and `@latest` silently resolves to the newest lower-major tag, so the
published install command in the README, `install.sh`, `SKILL.md` and the fail-closed
hook's own "install it:" message can all stop installing anything, for everyone,
silently.

It hides because every path this repo exercises bypasses module resolution:
`just install` is `go install .` (local, no proxy) and CI is `checkout`+`vet`+`test`
(also local). A green gate and a working local binary say *nothing* about whether a
consumer can install the thing — only a fresh `go install <module>@latest` does.

So: **after any release that moves the module path or major version, verify from
outside the repo** — `GOPATH=$(mktemp -d) go install github.com/lockyc/docgraph/v3@latest`
in a temp dir; do not infer it from a green gate. And never "fix" a rejected major tag
by dropping to a lower major — the tags are public; move the module path forward to
match instead.

The pre-push hook `hookScript` generates must resolve docgraph via PATH **and** the
Go bin dir (`$GOBIN`/`$GOPATH/bin`/`~/go/bin`), not `command -v` alone
(`docgraphBinFunc`, `main.go`; guarded by `main_test.go`). GUI clients and sandboxed
agent harnesses push with a bare PATH that omits `~/go/bin`, so a `command -v`-only
lookup fail-closes the gate on a *present-but-invisible* binary — which reads as
"docgraph is broken" and trains agents to reach for `--no-verify`. Do not narrow the
Go-bin fallback back to `command -v`.

## v1 gaps (documented, not silent)

Anchor validity, external-URL liveness, raw `<a href>`, per-section `index.md`
implicit-nav, repo-specific conventions. Add only with a test and a README note.

Those are **non-goals**. Deferred-but-wanted work is a different list:
[`docs/FOLLOWUPS.md`](docs/FOLLOWUPS.md) — read it before designing a change to
the `index` view, which has a known shape limit (it emits a standalone page, so
it cannot single-source an index section *embedded* in a hand-written doc; a
real consumer is carrying that shadow knowingly).

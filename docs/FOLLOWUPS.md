---
type: reference
links:
  - rel: part-of
    to: CLAUDE.md
    note: the deferred-work register CLAUDE.md's v1-gaps section points into
---

# Follow-ups

Wanted work that is deferred, not out of scope. (Genuinely out-of-scope
non-goals live in the README's *Known gaps* — don't mix them: a gap says "we
won't", this file says "not yet".)

## Stale by fact, not by calendar

`stale` compares a hand-written `verified:` date against a cadence (`review:`,
else the caller's default). That is a **proxy**, and it is noisy in both
directions: a doc for frozen code nags the day its timer expires, while a doc for
code that churned last week stays "fresh" until the timer happens to run out. It
also cannot tell a truthful `verified:` from a lie — nothing validates the date,
so the field is honour-system metadata.

`verified` + `covers` + git history answers the question the cadence only
approximates: **was this doc last verified _before_ the code it covers last
changed?** That is a fact, not a timer, and every input already exists —
`StaleDocs` simply never consults git today.

Deferred, not out of scope, for two reasons:

- **It needs populated `covers` edges to mean anything.** The covers-aware
  doc-drift rider is what creates the incentive to declare them; until repos
  actually have edges, this check has nothing to join against. Build it after
  there is real-world evidence the edges get declared.
- **It adds a cost the other views don't have.** `covers`/`index`/`stale` are
  currently pure reads over parsed frontmatter. This one needs a `git log -1`
  per covered path, so a repo with many edges pays per-path git invocations on a
  view that is expected to be instant. Batching (one `git log --name-only` walk,
  or `--since` the oldest `verified`) needs designing, not assuming.

Shape not chosen. Two candidates:

1. **Fold into `stale`** — a doc is stale if the calendar says so **or** its
   covered code moved since `verified`. One concept, one command; but it
   conflates two quite different claims in one output.
2. **A distinct class** (`stale --since-covered`, or a separate finding kind in
   the same output) — keeps "your timer expired" and "your code moved" legible as
   different facts, at the cost of more surface.

Note either way `stale` stays a **read-only view** (exit 0, never a gate) unless
that decision is revisited on its own merits.

## Superseded-link detection

If `B` declares `supersedes: A`, then any *other* doc still linking to `A` points
an agent at guidance the repo has already declared void. That is a real trap:
docgraph's whole premise is that an agent traverses the link graph, and this is
the graph leading it somewhere the owner has explicitly retired.

The relation is already in the vocabulary and already parsed — `supersedes`
currently exists **only** to be cycle-checked (`cycleRels`, `internal/audit/edges.go`).
Nothing reads it for meaning. Detection needs no new vocabulary and no new inputs:
the superseded set is `{e.To | e.Rel == "supersedes"}`, and the offenders are docs
whose links (markdown or frontmatter) resolve into that set, excluding the
superseding doc itself, which must be allowed to reference what it replaces.

It also mechanically enforces a rule that currently lives only in prose: *the old
way is void, not a constraint*.

Deferred because the design question is unresolved and the vocabulary is barely
used in the wild today:

- **Blocking check or advisory?** Unlike the covers rider, this one *does* judge —
  a link into a superseded doc is unambiguously wrong, which argues for a real
  `docgraph .` check. But docgraph enforces by default and excludes explicitly, so
  the day it lands every repo with `supersedes` edges gets a **failing gate** with
  no warning. That is the correct model working as intended, and still deserves a
  deliberate decision rather than a surprise.
- **What about the superseded doc's own outbound links?** A retired doc linking to
  other retired docs is not a finding worth having; scoping needs thought.
- **Interaction with `orphans`.** A superseded doc that nothing may link to
  becomes, by construction, an orphan. The two checks would then contradict each
  other — one demanding a link, the other forbidding it. Resolve this before
  building: most likely a superseded doc is exempt from `orphans`, but that is a
  decision, not an obvious consequence.

## Generated index as an *embedded section*, not only a standalone page

`docgraph index` emits a whole page (`# Index`, `## <type>` groups) intended to
be redirected into a tracked file. That shape cannot single-source the common
real case: **a hand-written doc whose index is one section among prose.**

The driving case (a private client app, 2026-07): its `CLAUDE.md` carries a *Reference
Documentation* section listing all 39 `docs/*.md` with a description each. It is
the same shadow as homelab's `tools.md` *Docs* column — a hand-maintained copy
of a relation the graph already holds, which nothing validates and which rots
the first time a doc is added or renamed. But it can't be fixed the same way
tools.md was:

- The section lives **inside** `CLAUDE.md`, surrounded by prose that must stay.
  `index > docs/index.md` would produce a *second* page, not replace the
  section — so the shadow would remain, now with a rival.
- Its per-doc descriptions are **richer than a one-line `description:`** — some
  are a paragraph carrying real guidance ("Read before touching `sortHierarchy`
  …"). Migrating them into frontmatter YAML makes prose that is edited often
  harder to edit, which is a real cost, not a purity nit.

So the shadow stands in that repo, knowingly, until this is resolved. Two candidate
shapes, neither chosen:

1. **Managed region** — `index --into <file> --marker <name>` rewrites only the
   text between two sentinel comments, leaving the rest of the file untouched.
   Fixes the embedding problem; still needs the descriptions in frontmatter.
2. **Leave the prose in the doc** — generate only the *structure* (which docs
   exist, grouped by type) and let each entry's description stay hand-written.
   Cheaper, but only single-sources half the relation, so the list can still
   drift in exactly the way that matters least (descriptions) and not at all in
   the way that matters most (membership).

The prerequisite for either is that a repo's docs carry `type` (that repo's now do).
Decide the shape before building — a half-generated section that still needs
hand-editing may be worse than an honest hand-written one.

## Publish the `graph` payload's own JSON Schema (`graph --schema`)

`docgraph schema` emits a JSON Schema for the *frontmatter* vocabulary, so a
consumer conforms to the check rules instead of re-encoding them. The `graph
--json` payload (`GraphView`, `GraphSchemaVersion = 1`) has **no** equivalent: a
consumer ingesting it — a catalog builder, Mycelium — has only the prose in the
README's `graph` section and the `schemaVersion` integer, not a machine-checkable
contract for `nodes`/`contentEdges`/`metadataEdges`/`islands`.

Deferred, not out of scope: the payload shape is single-sourced in
`internal/audit/graphview.go` today, and there is exactly one consumer (Mycelium,
below), which isn't built yet. Publish the schema when a second consumer appears
or the shape stabilises enough to freeze — emitting it *now* would just add a
second thing to keep in lockstep with a shape still settling. Likely surface:
`docgraph graph --schema` (mirrors `docgraph schema`), reading the same struct so
schema and payload can't drift.

## Mycelium ingestion of the `graph --json` seam (Spec 2)

The `graph --json` payload is built to be the seam the Mycelium ecosystem graph
ingests — per-repo doc structure feeding the cross-repo map. That consumer is a
separate project (Spec 2), not this repo's work: docgraph's responsibility ends at
emitting a stable, `schemaVersion`-stamped payload. Tracked here so the seam's
purpose isn't lost — the camelCase keys, the always-present empty arrays, and the
`schemaVersion` bump-don't-reshape rule all exist for this consumer, and a change
to the payload must weigh it even though it lives elsewhere.

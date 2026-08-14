# `default` pipes `just --list` through a small stock-perl filter that clips long recipe
# docs to your terminal width (…) instead of wrapping. Self-contained — no external files;
# falls back to plain `just --list` where perl is absent. Edit the recipes below, not this.
# List available recipes
default:
    @if command -v perl >/dev/null 2>&1; then just --color always --list | perl -CS -Mutf8 -lpe 'BEGIN{($w)=`stty size 2>/dev/null </dev/tty`=~/ (\d+)/; $w||=100; $col=(-t STDOUT && !exists $ENV{NO_COLOR})} s/\e\[[0-9;]*m//g unless $col; (my $v=$_)=~s/\e\[[0-9;]*m//g; if(length($v)>$w){my($o,$n)=("",0); while(length && $n<$w-1){ if($col && s/^(\e\[[0-9;]*m)//){$o.=$1}else{s/^(.)//;$o.=$1;$n++} } $_=$o."…".($col?"\e[0m":"")}'; else just --list; fi

build:
    go build -o docgraph .

test:
    go test ./...

# install to the Go bin dir, then exec the new binary once.
# That exec is LOAD-BEARING, not a smoke test — see the cold-exec footgun in CLAUDE.md:
# a freshly written binary has a new cdhash, so its FIRST exec blocks in dyld for a live
# code-signature assessment (~1s for this binary on macOS). Absorbing it here, where you
# are already waiting on a build, keeps it off the next doc-drift Stop hook.
install:
    #!/usr/bin/env bash
    set -euo pipefail
    go install .
    bin="$(go env GOBIN)"; [ -n "$bin" ] || bin="$(go env GOPATH)/bin"
    "$bin/docgraph" version

fmt:
    gofmt -w .

# non-mutating pre-release gate: gofmt check + vet + tests
gate:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
      echo "✗ gofmt: these files need formatting:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    go vet ./...
    go test ./...
    echo "✓ gate passed"

# cut the release for the current VERSION: fast-forward main → tag v<VERSION> → GitHub release.
# run on dev with VERSION bumped and committed; the tree must be clean and gate-green.
# NOTES is required and hand-written (default RELEASE_NOTES.md, gitignored). `gh
# --generate-notes` used to fill this in and was removed: it summarises MERGED PRs, and this
# repo integrates directly on the trunk, so it only ever emitted a bare "Full Changelog"
# link — a release with no notes, which the release model forbids. Write them; don't
# reintroduce the flag.
release notes="RELEASE_NOTES.md":
    #!/usr/bin/env bash
    set -euo pipefail
    version="$(tr -d '[:space:]' < VERSION)"
    tag="v${version}"
    # Check the notes FIRST: everything below this point (tag, push, release) is
    # public and awkward to retract, so a missing notes file must fail before any
    # of it happens, not between the tag push and the release create.
    if [ ! -s "{{notes}}" ]; then
      echo "✗ no release notes at '{{notes}}' — write them, then re-run." >&2
      echo "  Summarise what shipped since the previous tag (features / fixes / docs)," >&2
      echo "  leading with anything that changes how consumers install or invoke docgraph." >&2
      echo "  Pass a different path with: just release <file>" >&2
      exit 1
    fi
    if [ -n "$(git status --porcelain)" ]; then
      echo "✗ working tree is dirty — commit the VERSION bump first" >&2
      exit 1
    fi
    if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
      echo "✗ tag ${tag} already exists — bump VERSION before releasing" >&2
      exit 1
    fi
    just gate
    git push origin dev
    # main only ever fast-forwards to a release commit. Assert it hasn't diverged
    # (an out-of-band commit on main) so we fail loud here instead of silently
    # rewinding main and losing that commit at the next release.
    if ! git merge-base --is-ancestor main dev; then
      echo "✗ main is not an ancestor of dev — it diverged; back-merge main into dev first" >&2
      exit 1
    fi
    git branch -f main dev
    git push origin main
    git tag -a "${tag}" -m "${tag}" main
    git push origin "${tag}"
    gh release create "${tag}" --target main --title "${tag}" --notes-file "{{notes}}"
    echo "✓ released ${tag}"

#!/usr/bin/env bash
# install.sh — installs/updates the docgraph binary via `go install`.
# Usage:  bash install.sh
#    or:  curl -fsSL https://raw.githubusercontent.com/lockyc/docgraph/main/install.sh | bash
#
# The curl URL requires the GitHub repo to be PUBLIC (docgraph is).
#
# docgraph is a Go CLI, not a bundle — this installs a BINARY to the Go bin dir; there
# is no ~/.docgraph clone to manage. It seeds the global config dir but never edits
# ~/.claude/settings.json or any repo's git hook — that guided wiring is /docgraph:install's
# job (a non-interactive curl|bash must not silently edit global config). Never uses `just`.
set -e

# The /v2 suffix is Go's semantic import versioning, not decoration: a module at
# major >=2 MUST declare it, or the proxy rejects every v2 tag and `@latest`
# silently falls back to the newest v1 — which is how the published install path
# broke once already. It is also the go.mod module line verbatim, so the IN_REPO
# grep below and the @latest install stay one source.
MODULE="github.com/lockyc/docgraph/v3"

command -v go >/dev/null 2>&1 || {
  echo "docgraph: Go is required (https://go.dev/dl/). Install it, then re-run." >&2
  exit 1
}
command -v git >/dev/null 2>&1 || \
  echo "docgraph: warning — git not found on PATH; docgraph shells out to git at runtime." >&2

# IN_REPO when run from a checkout: build the current tree so uncommitted work is installed.
# NOT_IN_REPO: install the published module at @latest.
if [ -f go.mod ] && grep -q "^module $MODULE\$" go.mod 2>/dev/null && [ -f main.go ]; then
  echo "Installing docgraph from the current checkout (go install .) ..."
  go install .
else
  echo "Installing docgraph from $MODULE@latest ..."
  go install "$MODULE@latest"
fi

# Resolve the Go bin dir the binary landed in.
BIN_DIR="$(go env GOBIN)"
[ -n "$BIN_DIR" ] || BIN_DIR="$(go env GOPATH)/bin"
[ -n "$BIN_DIR" ] || BIN_DIR="$HOME/go/bin"

# Seed the global config dir (never overwrite existing rules/config).
CFG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/docgraph"
mkdir -p "$CFG_DIR"

# `docgraph version` prints "docgraph <ver>"; keep just the version token.
# This exec is also LOAD-BEARING beyond the version string — see the cold-exec footgun in
# CLAUDE.md: the first exec of a freshly written binary blocks in dyld for a live
# code-signature assessment (~1s on macOS). Running it here absorbs that while the user is
# already waiting on the install, instead of on their next doc-drift Stop hook. Do not
# "simplify" this to reading the VERSION file — that would silently reinstate the toll.
VERSION="$("$BIN_DIR/docgraph" version 2>/dev/null | awk '{print $NF}' || echo "")"

echo ""
echo "docgraph${VERSION:+ v$VERSION} installed → $BIN_DIR/docgraph"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "  warning — $BIN_DIR is not on your PATH; add it so 'docgraph' resolves." >&2 ;;
esac
echo ""
echo "Next steps:"
echo "  • Guided setup (Claude Code): /docgraph:install"
echo "      wires the doc-drift Stop hook into ~/.claude/settings.json, offers the"
echo "      per-repo pre-push gate, and seeds the leaks config."
echo "  • Pre-push gate for a repo:   run 'docgraph install-hook' inside it."
echo "  • doc-drift Stop hook (manual): add '$BIN_DIR/docgraph doc-drift' to the"
echo "      Stop hooks array in ~/.claude/settings.json."
echo "  • Leak rules are a global file you curate: $CFG_DIR/leaks.toml"
echo "      (schema + 'docgraph leaks-rules' export — see the README leaks section)."

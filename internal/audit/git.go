package audit

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GitRoot resolves path's work-tree root. On failure the error carries git's own
// stderr (gitError): a linked worktree whose `.git` gitdir pointer no longer
// resolves fails here and is otherwise indistinguishable from "not a repo" —
// only git's message names the pointer target as the cause.
func GitRoot(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", gitError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitError replaces an exec failure with the stderr git wrote, when it wrote any.
// exec.Cmd.Output() stashes it on *ExitError and it is the only place the real
// cause appears; "exit status 128" on its own is worse than useless.
func gitError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return fmt.Errorf("%s", msg)
		}
	}
	return err
}

func gitLines(root string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return nil, gitError(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, filepath.ToSlash(l))
		}
	}
	return lines, nil
}

func trackedMD(root string) ([]string, error) {
	return gitLines(root, "ls-files", "*.md")
}

func untrackedMD(root string) ([]string, error) {
	return gitLines(root, "ls-files", "--others", "--exclude-standard", "*.md")
}

// gitRawLines runs git and splits stdout on "\n" WITHOUT trimming, slash-
// converting, or dropping empty lines — required for diff parsing where '+'/'-'
// prefixes and blank added lines are significant.
func gitRawLines(root string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return nil, gitError(err)
	}
	s := string(out)
	s = strings.TrimSuffix(s, "\n") // drop only the trailing record separator
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// changedMarkdown lists .md files changed in the given range (e.g. "A..B").
func changedMarkdown(root, rng string) ([]string, error) {
	return gitLines(root, "diff", "--name-only", rng, "--", "*.md")
}

// changedCode lists the non-prose ("code") files changed in the given diff spec.
// It shares nonCodePathspec with gitDiff/stillDefinedInCode on purpose: all three
// answer questions about the same "what is code?" set, so a divergent definition
// here would report drift against a file class the diff never scanned.
func changedCode(root, spec string) ([]string, error) {
	args := append([]string{"diff", "--name-only", spec, "--"}, nonCodePathspec...)
	return gitLines(root, args...)
}

// addedLines returns the set of new-file line numbers added to path in rng, by
// parsing unified-diff hunk headers and counting '+' lines from the new start.
func addedLines(root, rng, path string) (map[int]bool, error) {
	lines, err := gitRawLines(root, "diff", "--unified=0", rng, "--", path)
	if err != nil {
		return nil, err
	}
	added := map[int]bool{}
	newLine := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "@@") {
			// @@ -a,b +c,d @@
			plus := strings.Index(l, "+")
			if plus < 0 {
				continue
			}
			rest := l[plus+1:]
			end := strings.IndexAny(rest, " ,")
			if end < 0 {
				end = len(rest)
			}
			start, e := strconv.Atoi(rest[:end])
			if e != nil {
				continue
			}
			newLine = start
			continue
		}
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			added[newLine] = true
			newLine++
		} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
			// deletion: new-file line does not advance
		} else {
			// context (none with --unified=0) advances new line
			newLine++
		}
	}
	return added, nil
}

// trackedAtRef lists the tracked "*.md" paths at ref from the object store, so it
// works on a bare repo (no work-tree). It enumerates all files at the ref via
// ls-tree and filters to .md — the same effective set trackedMD's "*.md" pathspec
// yields in a checkout, without depending on pathspec-glob semantics.
func trackedAtRef(gitDir, ref string) ([]string, error) {
	lines, err := gitLines(gitDir, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range lines {
		if strings.HasSuffix(l, ".md") {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out, nil
}

// fileAtRev returns path's content at rev; ok=false if it doesn't exist there.
func fileAtRev(root, rev, path string) (string, bool) {
	out, err := exec.Command("git", "-C", root, "show", rev+":"+path).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// ClosestBase returns the merge-base of head with the nearest integration branch
// (fewest commits in base..head). Used only for a new-branch push with no upstream.
func ClosestBase(root, head string) (string, bool) {
	best, bestCnt := "", -1
	for _, cand := range []string{"origin/HEAD", "main", "master", "dev", "develop", "trunk"} {
		mb, err := exec.Command("git", "-C", root, "merge-base", head, cand).Output()
		if err != nil {
			continue
		}
		base := strings.TrimSpace(string(mb))
		if base == "" {
			continue
		}
		cntOut, err := exec.Command("git", "-C", root, "rev-list", "--count", base+".."+head).Output()
		if err != nil {
			continue
		}
		cnt, err := strconv.Atoi(strings.TrimSpace(string(cntOut)))
		if err != nil {
			continue
		}
		if bestCnt < 0 || cnt < bestCnt {
			best, bestCnt = base, cnt
		}
	}
	return best, best != ""
}

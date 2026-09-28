#!/bin/sh
# Three things that must never reach a public tree, checked on every push.
#
# None of these is a typo people make. Each one was written deliberately, by a
# tool or by a person with the string in hand, which is why a reminder does not
# prevent them and a gate does:
#
#   1. a per-machine settings file, tracked      -> publishes whatever it holds
#   2. a credential-shaped assignment            -> publishes a live secret
#   3. a reference to a repo a reader cannot open -> a citation nobody can follow
#   4. the pre-rename name, outside where it belongs -> a build break or a lie
#
# Rule 3 is the softest and the most useful. A comment that points at an issue
# in some other tracker is worthless to anyone reading this source, and the
# allow-list below is the whole definition of "some other": add a repo to it the
# first time this codebase has a legitimate reason to cite it.
#
#   ./scripts/check-tree.sh
#
# Only tracked files are scanned, so anything gitignored is out of scope by
# construction. Exit 1 on the first rule that fires, naming the rule and the
# lines.
set -e

self="scripts/check-tree.sh"
status=0

fail() {
	echo "::error::$1"
	shift
	printf '%s\n' "$@"
	status=1
}

# --- 1. per-machine settings ------------------------------------------------
#
# A *.local.* config is one developer's machine: their paths, their hosts, and
# in the case that prompted this, their password. The .local infix is the
# convention for exactly that, so tracking one is always a mistake.
local_files=$(git ls-files | grep -E '(^|/)[^/]*\.local\.(json|ya?ml|toml)$' || true)
if [ -n "$local_files" ]; then
	fail "per-machine settings are tracked; add them to .gitignore and git rm --cached" "$local_files"
fi

# --- 2. credential shapes ---------------------------------------------------
#
# An assignment to a secret-ish name whose value is neither empty nor a
# placeholder. Placeholders are what keeps this quiet on documentation and on
# code that builds a request: $VAR, <token>, {{ }}, %s, ..., and "" all pass.
creds=$(git grep -InE \
	'(pass|passwd|password|secret|token|api[_-]?key)=[^"'"'"'[:space:]$<{%&]{6,}' \
	-- . ":(exclude)$self" || true)
if [ -n "$creds" ]; then
	fail "a credential-shaped value is in a tracked file" "$creds"
fi

# --- 3. references to repos a reader cannot open ----------------------------
#
# Both forms GitHub understands: the full URL, and the owner/repo#N or slug#N
# shorthand. A bare #N is same-repo and always fine.
allowed='gnoverse/gnoscope
gnolang/gno
gnolang/hackerspace
gnolang/tx-indexer
gnolang/meetings
gnolang/ecosystem-fund-grants
gnolang/memeland
gnolang/gnomobile
gnoswap-labs/gnoswap
TERITORI/gno
irreverentsimplicity/zentasktic-core'

urls=$(git grep -IohE 'github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/(issues|pull)/[0-9]+' \
	-- . ":(exclude)$self" 2>/dev/null |
	sed -E 's|.*github\.com/([^/]+/[^/]+)/.*|\1|' | sort -u |
	grep -vxF "$allowed" || true)
if [ -n "$urls" ]; then
	fail "linked issues in repos not on the allow-list in $self" "$urls"
fi

# A lowercase hyphenated slug immediately before #N is a cross-repo shorthand.
# Prose ("PR #12", "issue #3") is preceded by a space and does not match.
short=$(git grep -InE '(^|[^A-Za-z0-9_./-])[a-z0-9]+(-[a-z0-9]+)+#[0-9]+' \
	-- . ":(exclude)$self" || true)
if [ -n "$short" ]; then
	fail "cross-repo issue shorthand; write the full URL, or cite the code instead" "$short"
fi

# --- 4. the name this project had before 2026-09-28 -------------------------
#
# Transitional, and worth removing once nothing in flight predates the rename.
# It is here because the rename landed while other branches were open, and a
# branch cut before it reintroduces the old name on merge without anything
# complaining.
#
# The module path is the half that actually breaks, and it breaks in a way the
# obvious check misses: `go build ./...` does not compile test files, so an
# import in a _test.go sails through it. That happened on 2026-09-28 merging
# main into the rename branch, in two files at once. `go vet` does catch it;
# this catches it a step earlier and without a toolchain.
modpath=$(git grep -In 'github\.com/moul/mygnoscan' \
	-- . ":(exclude)$self" ":(exclude)docs/rename.md" || true)
if [ -n "$modpath" ]; then
	fail "the pre-rename module path is back; it is github.com/gnoverse/gnoscope" "$modpath"
fi

# The brand, allowed only where the rename itself is the subject: the record of
# it, the notice announcing it, and the code and tests that carry settings
# across it. index.html is allowed whole rather than by line because the
# migration needs the literal old key prefix; the storage half of that file is
# covered instead by pkg/web's TestEveryPersistedSettingIsMigratedFromTheOldName.
brand=$(git grep -In 'mygnoscan' \
	-- . ":(exclude)$self" \
	":(exclude)docs/rename.md" ":(exclude)README.md" ":(exclude)AGENTS.md" \
	":(exclude)e2e/tests/rename.spec.js" ":(exclude)pkg/web/web_test.go" \
	":(exclude)pkg/web/frontend/index.html" \
	":(exclude)pkg/store/contracts_bench_test.go" || true)
if [ -n "$brand" ]; then
	fail "the pre-rename name is outside the files that are about the rename; it is gnoscope" "$brand"
fi

if [ "$status" -eq 0 ]; then
	echo "tree clean: no per-machine settings, no credential shapes, no unopenable references, no pre-rename names"
fi
exit "$status"

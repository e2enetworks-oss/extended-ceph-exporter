#!/usr/bin/env bash
#
# Print the CHANGELOG.md section for a single release, so the GitHub Release body
# is the changelog rather than a second, hand-written summary that drifts from it.
#
# Section headings are the Prometheus convention this repository already uses:
#
#   ## 1.8.0 / 2025-11-18
#
# Usage: scripts/extract-changelog.sh <version> [changelog-path]
#   version         release version without the leading "v" (e.g. 1.9.0)
#   changelog-path  defaults to CHANGELOG.md relative to the current directory

set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: $0 <version> [changelog-path]" >&2
	exit 2
fi

version="$1"
changelog="${2:-CHANGELOG.md}"

if [ ! -f "$changelog" ]; then
	echo "error: no such changelog: $changelog" >&2
	exit 1
fi

# Print every line after the matching heading up to (but not including) the next
# "## " heading. The version is matched literally: awk's index() avoids a version
# like 1.9.0 being read as a regular expression where "." matches any character.
body="$(
	awk -v want="## ${version} / " '
		index($0, want) == 1 { found = 1; next }
		found && /^## / { exit }
		found { print }
	' "$changelog"
)"

# Strip leading and trailing blank lines so the release body starts at the first
# bullet.
body="$(printf '%s\n' "$body" | sed -e '/./,$!d' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')"

if [ -z "$body" ]; then
	echo "error: ${changelog} has no '## ${version} / YYYY-MM-DD' section" >&2
	echo "hint: rename the '## Unreleased' heading to '## ${version} / $(date -u +%Y-%m-%d)'" >&2
	exit 1
fi

printf '%s\n' "$body"

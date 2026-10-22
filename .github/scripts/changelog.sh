#!/usr/bin/env bash
# CHANGELOG.md drives releases: every merge to main releases the version
# its top section names ("## v1.2.3 — 2026-10-22").
#
#   changelog.sh version   the top section's version (v1.2.3), or nothing
#   changelog.sh notes V   version V's notes (the section's body)
#   changelog.sh check     for a pull request: the top section names a new
#                          version (above every tag) with a date
set -euo pipefail
file=${CHANGELOG:-CHANGELOG.md}
top=$(grep -m1 '^## ' "$file" || true)
version() { [[ $top =~ ^##\ (v[0-9]+\.[0-9]+\.[0-9]+) ]] && echo "${BASH_REMATCH[1]}" || true; }
case "${1:-}" in
  version) version ;;
  # The section's lines, without blank lines at either end.
  notes) awk -v v="$2" '$0 ~ "^## "v"( |$)" {f=1; next} /^## / {f=0} f' "$file" |
    awk 'NF {started=1} started {lines[++n]=$0} END {while (n > 0 && lines[n] == "") n--; for (i = 1; i <= n; i++) print lines[i]}' ;;
  check)
    v=$(version)
    last=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V | tail -1)
    fail() { echo "::error file=$file,line=1::$1" >&2; echo "✘ $1" >&2; exit 1; }
    next=v0.1.0
    if [[ $last =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then next="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] + 1))"; fi
    [[ -n $v ]] || fail "Every merge to main is a release: start CHANGELOG.md with its version and date, e.g. \"## $next — $(date +%F)\" (last release: ${last:-none})."
    [[ $top =~ \ —\ [0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || fail "Date the release: \"## $v — $(date +%F)\"."
    git rev-parse -q --verify "refs/tags/$v" >/dev/null && fail "$v is released already: give this change the next version (last: $last)."
    if [[ -n $last && $(printf '%s\n%s\n' "$last" "$v" | sort -V | tail -1) != "$v" ]]; then
      fail "$v is lower than the last release, $last."
    fi
    [[ -n $("$0" notes "$v") ]] || fail "Say what's in $v under its heading."
    echo "✔ releases $v on merge (last: ${last:-none})" ;;
  *) echo "usage: $0 version|notes V|check" >&2; exit 2 ;;
esac

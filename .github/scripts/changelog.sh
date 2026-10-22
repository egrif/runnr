#!/usr/bin/env bash
# What CHANGELOG.md says to release: the top section, if it names a version.
#
#   changelog.sh version      the top version (v1.2.3), or nothing if the
#                             top section is "## Unreleased" (no version yet)
#   changelog.sh dated        whether that section has a date (yes/no)
#   changelog.sh notes V      the notes of version V (the section's body)
#   changelog.sh date V DATE  set version V's date in CHANGELOG.md
set -euo pipefail
file=${CHANGELOG:-CHANGELOG.md}
top=$(grep -m1 '^## ' "$file" || true)
case "${1:-}" in
  version) [[ $top =~ ^##\ (v[0-9]+\.[0-9]+\.[0-9]+) ]] && echo "${BASH_REMATCH[1]}" || true ;;
  dated) [[ $top =~ [0-9]{4}-[0-9]{2}-[0-9]{2} ]] && echo yes || echo no ;;
  # The section's lines, without blank lines at either end.
  notes) awk -v v="$2" '$0 ~ "^## "v"( |$)" {f=1; next} /^## / {f=0} f' "$file" |
    awk 'NF {started=1} started {lines[++n]=$0} END {while (n > 0 && lines[n] == "") n--; for (i = 1; i <= n; i++) print lines[i]}' ;;
  date) awk -v v="$2" -v d="$3" '!done && $0 ~ "^## "v" " {print "## " v " — " d; done=1; next} {print}' "$file" > "$file.tmp" && mv "$file.tmp" "$file" ;;
  *) echo "usage: $0 version|dated|notes V|date V DATE" >&2; exit 2 ;;
esac

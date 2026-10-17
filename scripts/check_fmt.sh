#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
files="$(find . -path './.git' -prune -o -path './worktrees' -prune -o -path './bin' -prune -o -name '*.go' -type f -print | sort)"
unformatted="$(printf '%s\n' "$files" | xargs gofmt -l)"
[[ -z "$unformatted" ]] || { printf '%s\n' "$unformatted"; exit 1; }

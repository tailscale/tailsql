#!/usr/bin/env bash
#
# Usage: update-oss.sh <directory-path>
#
# Update the version of the tailscale.com module to be in sync with the version
# used by the specified repository.
#
# Requires: gh, git, go, jq
#
set -euo pipefail

# The repository path to compare to.
repo=tailscale/corp

# The module to update.
module=tailscale.com

cd "$(git rev-parse --show-toplevel)"
if ! git diff --quiet ; then
    echo ">> WARNING: The working directory is not clean." 1>&2
    echo "   Commit or stash your changes first." 1>&2
    git diff --stat
    exit 1
fi
git checkout --quiet main && git pull --rebase --quiet

have="$(go list -f '{{.Version}}' -m "$module" | cut -d- -f3)"
want="$(
  gh api -q '.content|@base64d' repos/"${repo}"/contents/go.mod |
  grep -E "\b${module}\b" | cut -d' ' -f2 | cut -d- -f3
)"
if [[ "$have" = "$want" ]] ; then
    echo "Module $module is up-to-date at commit $have" 1>&2
    exit 0
fi

go get "$module"@"$want"
go mod tidy
echo "Module $module updated to commit $want" 1>&2
echo "(you must commit and push this change to persist it)" 1>&2

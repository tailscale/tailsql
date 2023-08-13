#!/usr/bin/env bash
#
# Usage: update-oss.sh <directory-path>
#
# Update the version of the tailscale.com module to be in sync with the version
# used by the repo checked out in the specified directory.
#
# Requires: git, go, jq
#
set -euo pipefail

# The repository path to compare to.
repo="${1:?missing repository path}"

cd "$(dirname ${BASH_SOURCE[0]})/.."
if ! git diff --quiet ; then
    echo ">> WARNING: The working directory is not clean." 1>&2
    echo "   Commit or stash your changes first." 1>&2
    git diff --stat
    exit 1
fi
git checkout --quiet main && git pull --rebase --quiet

# The module to update.
module=tailscale.com

# The branch name to use when making an update.
branch="$USER"/update-oss-version

digest="$(
    cd "$repo"
    go list -json "$module" | \
        jq -r .Module.Version | \
        cut -d- -f3
)"
go get "$module"@"$digest"
go mod tidy
if git diff --quiet ; then
    echo "Module $module is up-to-date at commit $digest" 1>&2
else
    git checkout -b "$branch"
    git commit -m "go.mod: update $module to commit $digest" go.mod go.sum
    git push -u origin "$branch"
    echo "Module $module updated to commit $digest" 1>&2
    echo "Branch $branch created" 1>&2
fi

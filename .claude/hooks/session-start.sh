#!/bin/bash
# A cloud session starts with no pinned toolchain: the image's golangci-lint
# is built with an older Go and refuses this module, and the rest is absent
# until `task tools`. Local sessions already have it and are left alone.
set -euo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
	exit 0
fi

gobin="$(go env GOPATH)/bin"
export PATH="$gobin:$PATH"

# task is the only entry point; the version matches ci.yml. Everything else
# is pinned in the Taskfile, so it is not repeated here.
go install github.com/go-task/task/v3/cmd/task@v3.54.0
task tools

# Ahead of /usr/local/bin, or the image's stale golangci-lint still wins.
echo "export PATH=\"$gobin:\$PATH\"" >>"$CLAUDE_ENV_FILE"

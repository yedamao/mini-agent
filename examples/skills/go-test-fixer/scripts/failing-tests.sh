#!/bin/sh
# Runs go test in the current folder and prints only the failures.
# Usage: sh failing-tests.sh [packages]   (default ./...)
set -u
pkgs="${*:-./...}"
if ! command -v go >/dev/null 2>&1; then
  echo "error: go is not installed or not in PATH" >&2
  exit 2
fi
out=$(go test -count=1 $pkgs 2>&1)
status=$?
if [ $status -eq 0 ]; then
  echo "All tests pass:"
  echo "$out"
  exit 0
fi
# Failing tests and their messages (indented lines), plus build errors.
echo "$out" | grep -E '^(--- FAIL|FAIL|panic:|#)|^ {4}|\.go:[0-9]+:' || echo "$out"
exit $status

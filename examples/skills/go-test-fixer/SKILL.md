---
name: go-test-fixer
description: Step-by-step workflow for finding and fixing failing Go tests with minimal, verified changes. Use when the user says Go tests fail, asks to fix a failing test, or `go test` reports FAIL.
license: MIT
compatibility: Requires Go and a POSIX shell.
metadata:
  author: yedamao
  version: "1.0"
---

# Fixing failing Go tests

Goal: make the failing tests pass by fixing the **code under test**, with the
smallest change that is clearly correct.

## Steps

1. **See what fails.** Stay in the folder with the tests (don't cd into the
   skill folder) and run the helper script by its full path:
   `sh <skill folder>/scripts/failing-tests.sh`
   It prints only the failing tests and their messages (plain `go test ./...`
   works too).
2. **Read before editing.** Read the failing test and the function it calls.
   Work out what the test expects (got vs want) for each failing case.
3. **Find the root cause.** Look for the usual suspects:
   off-by-one loop bounds, `<` vs `<=`, wrong index, nil/empty input,
   swapped arguments, integer division, unhandled error.
4. **Fix the code, not the test.** Change the test only if it is clearly
   wrong, and say so. Never delete or skip a failing test to make it pass.
5. **Keep it minimal.** Change as little as possible; keep the style. Rewrite
   the whole file with write_file only after reading it in full.
6. **Verify.** Run `go test ./...` again (and `go vet ./...`). Repeat steps
   2-5 until everything passes.
7. **Report.** One short paragraph: the root cause, the change (file and
   line), and the final `go test` result.

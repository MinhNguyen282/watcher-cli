# Testing

## Single entry point

`task check` runs `scripts/check.sh`. The same script is used locally and in
CI. Named modes are:

| Mode | What it runs |
| --- | --- |
| `static` | `go build ./...` and `go vet ./...` |
| `unit` | `go test -failfast ./...` |
| `integration` | tests matching `Integration` |
| `e2e` | server and agent end-to-end tests |
| `coverage` | `go test -coverprofile` plus `go tool cover -func` |
| `all` | `static` then `unit` |

## Narrowest failing check

When fixing a regression, run the narrowest check that would fail for that
regression first, then one negative control that proves the guard rejects an
invalid case. Only then run the broader suite.

## Negative controls

Every guard added to the tool pipeline, filesystem gate, or job lifecycle
must have a test that exercises the real path and proves the invalid case is
rejected. A test that only proves the happy path is not evidence for the
guard.

## Environmental failures

Some shell tests exercise POSIX utilities (`sh`, `sleep`, `cat`) that may not
exist on a Windows host. These are environment failures, not product
failures. Run the suite on a POSIX host or in CI when touching the shell
package. Do not mark an environmental failure as a pass; report it as
quarantined and keep the unit-scoped checks green.

#!/bin/sh
# Single local and CI entry point for project checks.
#
# Usage: scripts/check.sh [static|unit|integration|e2e|coverage|all]
set -eu

mode="${1:-all}"

run_static() {
	echo "== static =="
	go build ./...
	go vet ./...
}

run_unit() {
	echo "== unit =="
	go test -failfast ./...
}

run_integration() {
	echo "== integration =="
	go test -failfast -run 'Integration' ./...
}

run_e2e() {
	echo "== e2e =="
	go test -failfast ./internal/server -run 'TestE2E|TestServer|TestAgent'
}

run_coverage() {
	echo "== coverage =="
	profile="${COVERAGE_PROFILE:-coverage.out}"
	go test -coverprofile="$profile" ./...
	go tool cover -func="$profile"
}

case "$mode" in
	static) run_static ;;
	unit) run_unit ;;
	integration) run_integration ;;
	e2e) run_e2e ;;
	coverage) run_coverage ;;
	all)
		run_static
		run_unit
		;;
	*)
		echo "unknown check mode: $mode" >&2
		echo "expected: static, unit, integration, e2e, coverage, all" >&2
		exit 2
		;;
esac

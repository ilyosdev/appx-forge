#!/usr/bin/env bash
# Phase 30 — wait-for-condition helpers for chaos tests.
#
# The TS harness has its own waitForCondition() that drives most
# scenarios; this shell version exists for direct CLI use during manual
# repro of a chaos scenario without spinning up jest.

set -e

# Wait until $check_cmd exits 0 or $timeout seconds elapse.
# Polls every 1s. Returns 0 on success, 1 on timeout.
#
# Usage:
#   wait_for_condition "control plane up" "curl -sf http://localhost:8090/v1/healthz"
#   wait_for_condition "5 pool containers" "[ $(docker ps --filter name=pool- -q | wc -l) -ge 5 ]" 60
wait_for_condition() {
  local description="$1"
  local check_cmd="$2"
  local timeout="${3:-30}"
  local elapsed=0
  while ! eval "$check_cmd" >/dev/null 2>&1; do
    if [ "$elapsed" -ge "$timeout" ]; then
      echo "FAIL: $description after ${timeout}s" >&2
      return 1
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  echo "OK: $description in ${elapsed}s"
}

# Block until the full Forge stack reports healthy on its own healthchecks.
# Useful as the first step of any chaos scenario.
wait_for_stack_ready() {
  local timeout="${1:-120}"
  wait_for_condition "control plane healthz" \
    "curl -sf http://localhost:8090/v1/healthz" \
    "$timeout"
}

# Allow this file to be sourced (`source assert.sh`) for the helpers, OR
# invoked directly with a subcommand for ad-hoc waits from a Makefile.
case "${1:-}" in
  "")                        ;; # sourced — do nothing
  wait-stack)                wait_for_stack_ready "${2:-120}" ;;
  wait-condition)            wait_for_condition "$2" "$3" "${4:-30}" ;;
  *)
    echo "Usage: source $0   # for helper functions" >&2
    echo "       $0 wait-stack [timeout_seconds]" >&2
    echo "       $0 wait-condition <description> <check_cmd> [timeout_seconds]" >&2
    exit 1
    ;;
esac

#!/usr/bin/env bash
# Wait for a workflow run and verify EVERY job, not just the run.
#
# GitHub can report a run as completed/success while a job is still queued
# (observed 2026-07-23 with a macOS runner). Polling run-level status alone
# therefore produces a false green for a job that never executed.
set -euo pipefail

repo="${REPO:-dynaum/kubeside}"
run="${1:-$(gh run list -R "$repo" --limit 1 --json databaseId --jq '.[0].databaseId')}"
deadline=$(( $(date +%s) + ${TIMEOUT:-900} ))

while :; do
  jobs_json="$(gh api "repos/$repo/actions/runs/$run/jobs")"
  pending="$(jq -r '[.jobs[] | select(.status != "completed")] | length' <<<"$jobs_json")"
  if [ "$pending" -eq 0 ]; then break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "TIMEOUT: $pending job(s) never completed"
    jq -r '.jobs[] | "  \(.name): \(.status)/\(.conclusion // "none")"' <<<"$jobs_json"
    exit 2
  fi
  sleep 15
done

jq -r '.jobs[] | "  \(.name): \(.conclusion)"' <<<"$jobs_json"
failed="$(jq -r '[.jobs[] | select(.conclusion != "success")] | length' <<<"$jobs_json")"
if [ "$failed" -ne 0 ]; then
  echo "RESULT: $failed job(s) did not succeed"
  exit 1
fi
echo "RESULT: all jobs succeeded"

#!/usr/bin/env bash
# Report the rs-release pipeline result to the GitCode pull request as a comment.
#
# Invoked by the "rs-pr-report" CodeArts build task in the pipeline's
# post-processing stage (always runs, so failures are reported too):
#   sh .codearts/pr-report.sh
#
# Build-task parameters (set from the pipeline's post job):
#   GITCODE_TOKEN   GitCode personal access token (secret param, required)
#   MR_ID           PR number, from ${{ sources.RouteServer.merge_id }}
#   JOB_STATUSES    "<unit>,<e2e>,<build>" job statuses, from
#                   ${{ format('{0},{1},{2}', jobs.JOB_unit.status, ...) }}
#                   (COMPLETED / FAILED / CANCELED / INIT / ...)
# Optional:
#   CI_RESULT       explicit overall result override (SUCCESS / FAIL)
#
# Without MR_ID the script looks up the open PR whose head branch matches the
# checked-out branch; with no open PR (e.g. manual runs) it exits cleanly.
set -eu

REPO_OWNER="InferTurbo"
REPO_NAME="RouteServer"
API="https://api.gitcode.com/api/v5"
PIPELINE_URL="https://devcloud.cn-north-4.huaweicloud.com/cicd/project/48aa1cc519f34d39a0563e19418bbc1e/pipeline/history/0a83fefdda6b404f9f170f278eb0611f?from=in-project"

# CodeArts checks the code out into the working directory; tolerate subdirectory layout
if [ -d repo_workspace ]; then cd repo_workspace; fi

if [ -z "${GITCODE_TOKEN:-}" ]; then
    echo "[pr-report] GITCODE_TOKEN is not set; skip reporting" >&2
    exit 0
fi

# GIT_LOCAL_BRANCH is the PR source branch, codeBranch the merge target; on
# merge-request triggers CodeArts builds the source merged into the target
BRANCH="${GIT_LOCAL_BRANCH:-${CI_SOURCE_BRANCH:-}}"
if [ -z "$BRANCH" ] && command -v git >/dev/null 2>&1; then
    BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '')"
fi
[ "$BRANCH" = "HEAD" ] && BRANCH=""
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

echo "[pr-report] branch=${BRANCH:-<none>} target=${codeBranch:-<none>} commit=${COMMIT}" \
    "mr_id=${MR_ID:-<unset>} job_statuses=${JOB_STATUSES:-<unset>} run=${pipelineRunId:-<unset>}"

# An unevaluated expression means the pipeline did not substitute the param
case "${MR_ID:-}" in *'${{'*) MR_ID="" ;; esac

PR_NUMBER="${MR_ID:-}"
if [ -z "$PR_NUMBER" ]; then
    if [ -z "$BRANCH" ]; then
        echo "[pr-report] no branch and no MR_ID; skip reporting"
        exit 0
    fi
    PR_NUMBER="$(curl -sf --max-time 20 -H "PRIVATE-TOKEN: ${GITCODE_TOKEN}" \
        "${API}/repos/${REPO_OWNER}/${REPO_NAME}/pulls?state=open&per_page=50" |
        python3 -c "
import sys, json
branch = sys.argv[1]
for pr in json.load(sys.stdin):
    if (pr.get('head') or {}).get('ref') == branch:
        print(pr['number'])
        break
" "$BRANCH" || true)"
fi
if [ -z "$PR_NUMBER" ]; then
    echo "[pr-report] no open PR found for branch '${BRANCH:-<none>}'; skip reporting"
    exit 0
fi
echo "[pr-report] reporting to PR #${PR_NUMBER}"

BODY_FILE="$(mktemp)"
REPORT_BRANCH="${BRANCH:-unknown}" REPORT_TARGET="${codeBranch:-}" REPORT_COMMIT="$COMMIT" \
REPORT_URL="$PIPELINE_URL" REPORT_RUN="${pipelineRunId:-}" \
REPORT_STATUSES="${JOB_STATUSES:-}" REPORT_OVERRIDE="${CI_RESULT:-}" \
python3 - "$BODY_FILE" <<'PYEOF'
import json, os, sys

stages = ["Unit tests", "e2e tests", "Build & package"]
raw = os.environ.get("REPORT_STATUSES", "")
statuses = [] if "${{" in raw else [s.strip().upper() for s in raw.split(",")] if raw else []

mark = {
    "COMPLETED": "✅ Passed",
    "FAILED": "❌ Failed",
    "CANCELED": "⚠️ Canceled",
}
rows = []
for i, name in enumerate(stages):
    st = statuses[i] if i < len(statuses) else ""
    rows.append(f"| {name} | {mark.get(st, '⏭️ Not run' if st else '❔ Unknown')} |")

override = os.environ.get("REPORT_OVERRIDE", "").upper()
if override.startswith("SUCCESS") or (statuses and all(s == "COMPLETED" for s in statuses)):
    head = "## ✅ Pipeline passed"
elif override.startswith("FAIL") or "FAILED" in statuses:
    head = "## ❌ Pipeline failed"
elif "CANCELED" in statuses:
    head = "## ⚠️ Pipeline canceled"
else:
    head = "## ▶️ Pipeline executed (see the run link for details)"

branch = os.environ.get("REPORT_BRANCH", "unknown")
target = os.environ.get("REPORT_TARGET", "")
code_desc = f"`{branch}` merged into `{target}`" if target and target != branch else f"`{branch}`"
run = os.environ.get("REPORT_RUN", "")
lines = [
    f"{head} (rs-release)",
    "",
    "| Stage | Result |",
    "| --- | --- |",
    *rows,
    "",
    f"- Build: {code_desc} @ {os.environ.get('REPORT_COMMIT', 'unknown')}",
    f"- Details: [CodeArts pipeline run]({os.environ.get('REPORT_URL', '')})" + (f" (run ID `{run[:8]}`)" if run else ""),
    "",
    "<!-- rs-release-ci-report -->",
]
with open(sys.argv[1], "w", encoding="utf-8") as f:
    # non-ASCII body must go through a UTF-8 JSON file (curl -d mojibakes on Git Bash)
    f.write(json.dumps({"body": "\n".join(lines)}, ensure_ascii=False))
PYEOF

HTTP_CODE="$(curl -s -o /tmp/pr-report-resp.json -w '%{http_code}' --max-time 20 \
    -X POST -H "PRIVATE-TOKEN: ${GITCODE_TOKEN}" -H "Content-Type: application/json" \
    --data-binary @"$BODY_FILE" \
    "${API}/repos/${REPO_OWNER}/${REPO_NAME}/pulls/${PR_NUMBER}/comments")"
rm -f "$BODY_FILE"

if [ "$HTTP_CODE" = "201" ] || [ "$HTTP_CODE" = "200" ]; then
    echo "[pr-report] comment posted to PR #${PR_NUMBER}"
else
    echo "[pr-report] failed to post comment (HTTP ${HTTP_CODE}): $(head -c 300 /tmp/pr-report-resp.json)" >&2
    exit 1
fi

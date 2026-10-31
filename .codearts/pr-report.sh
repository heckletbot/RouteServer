#!/usr/bin/env bash
# Report the rs-release pipeline result to the GitCode pull request as a comment.
#
# Invoked by the "rs-pr-report" CodeArts build task in the pipeline's
# post-processing stage (configured to always run, so it reports failures too):
#   sh .codearts/pr-report.sh
#
# Required environment (set as build-task parameters):
#   GITCODE_TOKEN   GitCode personal access token (secret param)
# Optional:
#   CI_RESULT       overall pipeline outcome hint wired from pipeline params
#                   (SUCCESS / FAIL); when empty the script infers what it can
#   MR_ID           merge-request number on merge-request triggers
#
# The script discovers the open PR whose head branch matches the checked-out
# branch and posts one comment; without an open PR (e.g. manual runs on
# pipeline) it exits cleanly without commenting.
set -eu

REPO_OWNER="InferTurbo"
REPO_NAME="RouteServer"
API="https://api.gitcode.com/api/v5"
PIPELINE_NAME="rs-release"
PIPELINE_URL="https://devcloud.cn-north-4.huaweicloud.com/cicd/project/48aa1cc519f34d39a0563e19418bbc1e/pipeline/history/0a83fefdda6b404f9f170f278eb0611f?from=in-project"

# CodeArts checks the code out into the working directory; tolerate subdirectory layout
if [ -d repo_workspace ]; then cd repo_workspace; fi

if [ -z "${GITCODE_TOKEN:-}" ]; then
    echo "[pr-report] GITCODE_TOKEN is not set; skip reporting" >&2
    exit 0
fi

# --- discover the source branch of this run --------------------------------
# GIT_LOCAL_BRANCH is exported by CodeArts build tasks (verified in task env)
BRANCH="${GIT_LOCAL_BRANCH:-${CI_SOURCE_BRANCH:-}}"
if [ -z "$BRANCH" ] && command -v git >/dev/null 2>&1; then
    BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '')"
fi
[ "$BRANCH" = "HEAD" ] && BRANCH=""  # detached checkout
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo '${COMMIT_ID:-unknown}')"

# Debug: dump candidate variables once so the wiring can be verified in logs
echo "[pr-report] branch=${BRANCH:-<none>} commit=${COMMIT:-<none>} mr_id=${MR_ID:-<unset>} ci_result=${CI_RESULT:-<unset>}"
env | grep -Ei 'mr_id|stage|result|pipeline' | grep -v GITCODE_TOKEN | sed 's/^/[pr-report] env: /' || true

# --- find the open PR for this branch --------------------------------------
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
    head = pr.get('head') or {}
    if head.get('ref') == branch:
        print(pr['number'])
        break
" "$BRANCH" || true)"
fi
if [ -z "$PR_NUMBER" ]; then
    echo "[pr-report] no open PR found for branch '${BRANCH:-<none>}'; skip reporting"
    exit 0
fi
echo "[pr-report] reporting to PR #${PR_NUMBER}"

# --- build the comment body -------------------------------------------------
case "${CI_RESULT:-}" in
    SUCCESS|success|success*)
        ICON="✅"; VERDICT="流水线通过"
        ;;
    FAIL|FAILED|fail*|error*)
        ICON="❌"; VERDICT="流水线失败"
        ;;
    *)
        ICON="▶️"; VERDICT="流水线已执行（结果以链接为准）"
        ;;
esac
export REPORT_ICON="$ICON" REPORT_VERDICT="$VERDICT" REPORT_BRANCH="${BRANCH:-unknown}" \
    REPORT_COMMIT="$COMMIT" REPORT_URL="$PIPELINE_URL" REPORT_PIPELINE="$PIPELINE_NAME"

BODY_FILE="$(mktemp)"
python3 - "$BODY_FILE" <<'PYEOF'
import sys, os
path = sys.argv[1]
icon = os.environ.get("REPORT_ICON", "▶️")
verdict = os.environ.get("REPORT_VERDICT", "流水线已执行")
branch = os.environ.get("REPORT_BRANCH", "unknown")
commit = os.environ.get("REPORT_COMMIT", "unknown")
url = os.environ.get("REPORT_URL", "")
lines = [
    f"## {icon} {verdict}（{os.environ.get('REPORT_PIPELINE','rs-release')}）",
    "",
    f"- 分支：`{branch}` @ {commit}",
    f"- 详情：[CodeArts 流水线运行记录]({url})",
    "",
    "<!-- rs-release-ci-report -->",
]
with open(path, "w", encoding="utf-8") as f:
    f.write("\n".join(lines))
PYEOF

# non-ASCII body must go through a UTF-8 JSON file (curl -d mojibakes on Git Bash)
python3 - "$BODY_FILE" <<'PYEOF'
import json, sys
body = open(sys.argv[1], encoding="utf-8").read()
open(sys.argv[1], "w", encoding="utf-8").write(json.dumps({"body": body}, ensure_ascii=False))
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

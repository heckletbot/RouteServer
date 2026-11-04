# Post-rebase code and documentation audit

[English](implementation-audit.md) | [简体中文](implementation-audit.zh-CN.md)

Baseline: `dev` at `1333071`, incorporating `origin/intranet-sync` at `6e10898`. Review date: 2026-11-03.
This is a review/validation milestone; no production implementation, public guide correction, commit
or push is included. Existing uncommitted work remains outside this change.

Follow-up status: the findings below retain the pre-fix baseline and reproduction evidence. The user
subsequently approved documentation/deployment error fixes and confirmed HTTPS off by default. See
[progress](progress.md) for the implementation, regression results and remaining open items.

## 1. Scope and conclusion

The user requested code/document consistency review, actual Make packaging, documented deploy.sh
startup with mock_vllm, and a full E2E run. They subsequently chose this macOS host and excluded
ConfigMap functionality. No Linux environment is provisioned; no Kubernetes deployment or ConfigMap
acceptance is claimed. Preliminary ConfigMap-local checks completed before that clarification are
retained in scratch evidence only and do not establish acceptance in this report.

The current code and public documentation are **not fully consistent**. The documented singleton
PD/Hybrid command paths can start the real Make-built server and complete mock-backed inference,
but platform-command errors and failure-ordering defects prevent calling deploy.sh fail-fast or
claiming a Linux/production deployment pass. Full E2E results are recorded separately in [progress](progress.md).

## 2. Findings

| ID / priority | Evidence | Impact and recommended follow-up |
| --- | --- | --- |
| V-01 / P1 | [go.mod](../../../go.mod) requires Go 1.26.0; [build guide](../../build.md), [Chinese guide](../../build.zh-CN.md) and both contribution guides still say 1.25.0. [CodeArts bootstrap](../../../.codearts/build.sh) accepts >=1.25 and falls back to downloading 1.25.0 | The documented/prepared local toolchain is below the module requirement. Automatic toolchain switching may hide this when network is available; local/offline toolchains cannot rely on it. Align guidance and reviewed bootstrap requirements; this run used cached Go 1.26.8 |
| V-02 / P1 | Both [deployment guides](../../deploy.md) / [Chinese guide](../../deploy.zh-CN.md) say HTTPS/mTLS default true on four planes. [deploy.sh](../../../deploy/deploy.sh) defaults the four HTTPS switches false and the mTLS switches true | Users are told the wrong effective transport defaults and that missing certificates alone block the default path. Document effective HTTPS/mTLS interaction and explicit secure/demo choices; do not silently revert the upstream policy in a review |
| V-03 / P1 | Real negative invocations copy binaries/pipelines before missing-main-certificate rejection. Invalid `--host` makes config_generator fail, but deploy.sh still attempts a server start before ultimately exiting 1 | Invalid input can already mutate an installation; generation failure is not an immediate stop. Move validated prerequisites before mutation and explicitly stop on failed generation/copy/start in a separately approved fix. The reproduced negative cases returned nonzero; do not misreport them as false-success exits |
| V-04 / P2 | Both [security guides](../../security.md) / [Chinese guide](../../security.zh-CN.md) use `--client-https-enable=true` / `--etcd-tls-enable=true`. Real deploy.sh rejects the equals form as `Unknown option`; its parser expects separate arguments | Published example commands cannot run verbatim. Correct both languages to the actual parser or separately approve parser changes; do not confuse Go flag parsing with deploy.sh parsing |
| V-05 / P2 | Security guides label a worker example but pass `--candidate true`; deploy guides describe worker=false versus candidate=true | The command allows leadership rather than configuring the documented worker role. Clarify intended role and update matching bilingual examples; no role-policy choice or live cluster test is made here |
| V-06 / P2 | Native deploy logs contain missing `nproc` and BSD/GNU `sed -i` errors, yet the script reaches the PID check and returns 0 | This host can serve the tested default mock requests despite failed preparation commands. This is evidence of unchecked command failures, not proof that rewrites or all deployment modes work on macOS. Scope a prerequisite/failure-propagation fix before advertising portability |
| V-07 / P2 | The singleton deployment guide treats `/status` HTTP 200 as success. After marking the test Decode mock unhealthy, the real server still returned 200 with `status: fault`, while readiness returned 500 / `is_ready: false` | A transport status is not backend readiness or inference success. Require the expected role state/readiness and a bounded request in the bilingual verification instructions; no status-code/protocol change is proposed |
| V-08 / P2 | [worker-kill BeforeEach](../../../e2etest/route-server/worker_kill_test.go) unconditionally calls [UnblockTrafficToPort](../../../e2etest/route-server/distributed_utils_test.go), ignoring its error even when ROUTER_TEST_ENABLE_SU=false. This run logs failed sudo password/terminal requests | The non-privileged suite still attempts privileged cleanup. Gate cleanup and track test-owned firewall rules in a separate test fix; skipped cases and ignored cleanup errors are not firewall-test coverage |

P1 denotes an issue that should be resolved before advertising reliable guided deployment; P2 denotes
an actionable example/robustness issue. These priorities are review recommendations, not a new merge
policy or authorization to change deployment behaviour.

## 3. Actual Make and deploy checks

A disposable snapshot of all 300 tracked working-source files was made before testing. Tests and
builds ran there, not in the user's checkout; pre-existing output was not cleaned. `make clean` and
real `make package` succeeded. Version/branch/commit variables were supplied from the original Git
checkout because the snapshot has no .git directory; the artifact reports the expected `1333071` commit.

The actual ZIP was extracted into another temporary directory. Its package layout contains the binary,
config generator, YAML/pipeline assets and the new ConfigMap/RBAC files. Inclusion checks do not test
ConfigMap functionality. The extracted deploy.sh was byte-identical to the source script and was not patched.

Three native mock_vllm processes represented Prefill, Decode and Hybrid backends on loopback ports
61001/61002/61003. The documented singleton PD and Hybrid commands were used with real addresses,
temporary RS_INSTALL_DIR, explicit business/ops ports, all four HTTPS flags false and stdout enabled.
No system install directory, real model endpoint, cluster credentials or existing service was used.

For each mode, deployment exited 0, the version response matched the built commit, readiness returned
`is_ready: true`, `/status` reported running backends, and non-stream/stream chat requests returned
200 with the mock's expected content. Streaming terminated with `[DONE]`; non-stream usage recorded
four completion tokens. Prefill, Decode and Hybrid mock logs each show two corresponding chat calls,
confirming the PD handoff and Hybrid routing paths rather than checking only server liveness.

A separate fresh PD run then marked only its own Decode mock unhealthy. It reproduced V-07:
`/status` remained HTTP 200 with a fault body, while `/internal/worker/readiness` correctly reported
HTTP 500 / not ready. The additional positive requests and fault transition are recorded separately
in `status-negative-final-result.json`; the primary PD/Hybrid result is not overwritten.

The initial temporary checker incorrectly expected the mock's non-stream text to have the streaming
token suffix. It was corrected to the actual mock contract (`test` versus `test_0...`), then rerun in a
new directory; the initial result/logs remain available. No production code was changed to make the test pass.
All smoke/negative-case processes were terminated through run-owned process groups and waited on.

## 4. Additional checks and limits

`go vet -mod=readonly ./...` reports 21 copylock diagnostics. The same Go 1.26.8 command on an archived
pre-rebase `b2c2841` checkout produces the exact same 21 diagnostic lines. Vet remains failed; these
are not new rebase failures and were not fixed or waived. A whole-repository unit/race pass is not claimed.

The original deploy.sh `--help` now terminates with exit 0. The previous skill-design audit's
non-terminating-help finding describes its old `b2c2841` baseline and must not be carried forward as
a current defect. The uncommitted skill design records were preserved, not rewritten during validation.

The document scan covers 81 tracked/design/report Markdown files and 689 local links/anchors, with
no missing targets. Existing deploy.md/deploy.zh-CN.md have different heading structures because
the English guide contains additional deployment material; identifying that translation drift does
not validate the excluded ConfigMap functionality. The new report pairs are structurally synchronized.

The full non-root suite also exposes V-08: failed `sudo iptables -D` cleanup attempts are logged,
while the suite continues. No successful firewall change is evidenced on this host; do not interpret
the three privileged skips or ignored cleanup error as successful network-partition simulation.

Current Make is 3.81, below the guide's stated 3.82 minimum; Bash is 3.2.57 and host architecture is
darwin/arm64. Actual build success is evidence for this run, not a changed support matrix. macOS is the
user-selected test host; Linux, real engines/accelerators, production TLS, privileged iptables cases
and ConfigMap/Kubernetes integration remain outside this run's acceptance.

## 5. Evidence and remaining work

Scratch evidence is indexed by `/private/tmp/routeserver-post-rebase-validation.json`. Under its
validation directory, inspect `run.json`, `logs/make-package.log`, `doc-deploy-smoke-final-result.json`,
`negative-deploy-result.json`, `cli-check-result.json`, `logs/vet.log`, `logs/baseline-vet.log` and the
full E2E log/result. These temporary tools are not new supported repository commands.

Resolve V-01 through V-08 in a separate approved correction milestone and update both languages
together. A green mock/E2E result does not repair inconsistent docs or deployment failure ordering.
No skill decisions are approved by this validation. See [progress](progress.md) for final test totals,
unexecuted coverage and repository-state checks.

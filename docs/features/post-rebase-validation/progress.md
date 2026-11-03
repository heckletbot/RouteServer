# Post-rebase validation progress

[English](progress.md) | [简体中文](progress.zh-CN.md)

## 2026-11-03: Authorized scope and environment

The user requested review and actual Make/deploy.sh/mock_vllm checks plus full E2E. They chose this
macOS host and excluded ConfigMap functionality. Baseline: `1333071` / upstream `6e10898`.
No production fixes, skill implementation, commit or push are performed. Existing working files are preserved.

Host: macOS arm64, cached official Go 1.26.8, Make 3.81, Bash 3.2.57. A 300-file tracked working-source
snapshot is used; GOTOOLCHAIN=local, GOWORK=off, GOPROXY=off and GOSUMDB=off with isolated caches/temp
directories. No dependency/toolchain download or Linux runtime installation is needed. Full E2E runs
non-root with ROUTER_TEST_ENABLE_SU=false; privileged iptables cases are not available on this host.

## Actual changes

Only this bilingual progress and the [audit](implementation-audit.md) are added. Existing source,
deployment scripts/configuration, public guides and uncommitted skill documents are not edited.
Temporary reproduction/checking tools and process-owned installs remain outside the repository.

## Executed checks and results

| Check | Result |
| --- | --- |
| `make clean` in the isolated snapshot | Passed, 0.2s; user's original output was not cleaned |
| Real `make package` with original Git metadata supplied | Passed, 7.68s; actual ZIP extracted and layout/assets checked |
| `go build -mod=readonly -o <validation-dir>/mock_vllm ./cmd/mock_server` | Passed, 0.5s |
| Documented deploy.sh PD/Hybrid paths | Both exited 0 and passed version, readiness, status and non-stream/stream mock chat checks; macOS nproc/sed errors retained, not counted as Linux deployment acceptance |
| Mock backend logs | Prefill, Decode and Hybrid each received two chat calls; four client requests succeeded with expected JSON/SSE content |
| Missing certificate / bad host negative deployments | Both exited 1; confirmed install/pipeline writes before rejection and attempted startup after generator failure |
| `/status` versus readiness after own Decode mock is marked unhealthy | Reproduced HTTP 200 with `status: fault` versus readiness HTTP 500 / `is_ready: false`; the guide's HTTP-code-only success criterion is insufficient |
| `bash deploy.sh --help` / equals-form option | Help exited 0; `--client-https-enable=true` exited 1 as unknown option, reproducing the documented command mismatch |
| `go vet -mod=readonly ./...` | Failed with 21 copylock diagnostics, exactly reproduced on pre-rebase b2c2841 using the same Go 1.26.8; not passed |
| Full `go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m` | Exit 0 in 1,608.01s overall; suite: 123 passed, 0 failed, 0 pending, 3 privileged iptables cases skipped; all three build entrypoints and suite teardown passed |
| Non-root E2E cleanup limitation | BeforeEach still attempts sudo iptables cleanup and ignores its error; no successful firewall change or privileged network-test coverage is claimed |
| Document/scope/process cleanup checks | 81 Markdown files / 689 local links have no missing targets; both new bilingual pairs match. Existing deploy-guide heading drift remains a finding. All 340 initial file hashes, HEAD and index are preserved; gexec artifact root removed, no test-owned processes or smoke listeners remain |

The initial smoke assertion failure came from expecting streaming text in a non-stream mock response;
only the temporary checker was corrected, and the clean rerun passed both modes. Preliminary local
ConfigMap package/configuration probes completed before the scope clarification are retained in
scratch logs, excluded from functional acceptance, and no further ConfigMap validation is pursued.

## Evidence and remaining limits

The local manifest `/private/tmp/routeserver-post-rebase-validation.json` points to the isolated
source, logs, actual ZIP, temporary installs and JSON results. No whole-repository unit/race result,
Linux runtime, real model, production TLS or Kubernetes/ConfigMap acceptance is claimed. Existing
unrelated files and Git/index state were checked and preserved. V-01 through V-08 remain review
findings for a separately authorized correction milestone; this task does not repair them silently.

## Completion

The authorized macOS review, actual Make/deploy/mock checks and full non-root E2E run are complete.
The tested singleton PD/Hybrid paths and E2E passed within the stated limits. Code/document drift,
deployment failure ordering, ignored platform/cleanup errors and baseline vet findings remain open;
this is not an all-green repository or production certification. Only four new report Markdown files
remain untracked. No existing file was edited by this task, and nothing was staged, committed or pushed.

## 2026-11-03: approved documentation and deployment fixes

### Decisions and scope

The user approved documentation/deploy.sh error fixes, regression coverage and bilingual updates.
HTTPS defaults to off; production enablement is a documentation recommendation. ConfigMap functionality,
existing vet findings and V-08 privileged E2E cleanup remain deferred. This milestone does not implement
skills, deploy production, stage, commit or push. The review and full E2E above remain pre-fix evidence.

### Actual changes and acceptance items

- V-01: README and build/contribution guides now match go.mod's Go 1.26.0
  minimum. CodeArts version detection/download fallback use 1.26, with an old-toolchain rejection regression.
- V-02: the four deploy.sh HTTPS defaults remain false. Main, ops, vLLM, etcd and peer HTTPS in the shipped
  template are also false, so direct generation does not implicitly enable TLS. mTLS switches remain
  conditional on HTTPS. Only four config-generator help descriptions change; TLS implementation stays
  intact. Bilingual configuration references/guides describe the defaults and explicit production enablement.
- V-03, V-06: prepare resources and run the existing generator in private staging. Copy, sed, generation and
  process-enumeration errors stop immediately. Required server credentials and supplied enabled-plane
  files are checked first; generator success without a new config is rejected instead of using a stale
  packaged file. Preparation failure neither writes installation files, stops the old instance nor starts
  a new one; staging is cleaned automatically.
- After generation, preserve the old config as `.bak` and match the old instance by its final executable
  path. No empty kill invocation; failed stop or a 10-second timeout aborts installation without force-kill.
  Use getconf for CPU count and GNU/BSD-compatible sed; preserve final log paths and daemon/child status semantics.
- V-04, V-05: security examples use separate option values and worker candidate=false. Production examples
  explicitly enable the relevant TLS planes; the etcd example uses etcd mode/endpoints. Credentials alone
  do not enable HTTPS.
- V-07: bilingual deployment checks require `/status` body inspection, worker readiness and a bounded
  request using a served model. Neither HTTP 200 alone nor a startup PID message establishes inference success.

### Regression execution and results

The same macOS / Go 1.26.8 host used an external source snapshot and temporary installations, with no real
model or cluster connections.

| Command/check | Result |
| --- | --- |
| `bash -n deploy/deploy.sh`, `bash -n .codearts/build.sh` | Passed |
| `python3 .codearts/test_deploy.py -v` | 14 passed: preparation preservation, missing config, certificates, copy/sed, CPU, process enumeration, stop failure/timeout, exact process selection, pipeline/metrics rewrites, backup, default HTTP, startup failure and daemon status |
| `python3 .codearts/test_makefile.py -v` | 8 passed: existing status/log/package checks plus Go 1.25 rejection and 1.26 fallback URL; fixture stops the download, no network install |
| `go test -mod=readonly ./cmd/config-tool -run 'Test_(shippedTemplateHTTPSDefaults\|applyTLSConfig\|applyOpsTLSConfig\|applyClientTLSConfig\|applyEtcdTLSConfig\|deployDefaultBehavior)$' -count=1 -v` | 6 top-level tests and their subtests passed; actual shipped-template generation without TLS arguments yields false at all five HTTPS locations; no ConfigMap tests run |
| Snapshot `make package VERSION=v0.1.0-alpha.1-16-g1333071-dirty BRANCH=dev COMMIT=13330714065d2e4be5e291e2b5eec636f1ddf4a1` | Passed; rebuilt final template/script/generator package and extracted it for checks |
| Final package `bash deploy.sh` singleton PD/Hybrid + mock_vllm, no HTTPS switches | Both exit 0; version, readiness, backend status and four stream/non-stream requests passed; no nproc/sed/empty-kill errors; logs use final installation paths |
| Real missing-main-certificate / invalid-host cases | Nonzero exits, no instance installation created, no binaries/pipelines copied, no startup attempted |
| Repeated deployment of this run's temporary PD instance | Passed: old instance identified/stopped, `.bak` retained, replacement ready and both request modes passed; used the first fixed package before template alignment, with the same script |
| Documentation/scope/cleanup checks | 81 Markdown files / 697 local links valid; security code blocks identical, audit/progress headings and finding IDs aligned; 322 original out-of-scope files, HEAD and index unchanged; all run-owned server/mock processes cleaned |

The new template-generation test initially omitted the config target directory required by the generator;
fixing the fixture made it pass without weakening production validation. The 123 passed / 3 skipped full
E2E result above belongs to the pre-fix run. This milestone did not repeat the entire approximately
27-minute suite; focused deployment/TLS regressions and real packaging/redeployment/inference cover the
changes. Historical results are not represented as a new post-fix full-suite execution.

### Completion and remaining boundaries

V-01 through V-07 are corrected within this scope and verified as above. V-08 and the existing 21 vet
copylock diagnostics remain open, unchanged and not reclassified as passing. ConfigMap implementation,
manifests and its dedicated copy branch remain unchanged; no Kubernetes/ConfigMap deployment or acceptance.
Native Linux, real models and production TLS handshakes are not accepted. Stopping/installing after
preparation is not an atomic transaction and provides no rollback. Readable credentials do not establish
runtime chain, identity or passphrase validity.

Local `/private/tmp/routeserver-deploy-fix.json` locates final build, regression, smoke and negative-case
evidence. All changes remain in the working tree; HEAD/index are unchanged, with no commit or push.

## 2026-11-03: full E2E rerun before commit

The user additionally authorized a complete E2E rerun against the fixed source and a standards-compliant
local commit after success, with no push. ConfigMap functionality and existing vet issues remain excluded.
This section supersedes the previous phase's no-commit and no-post-fix-full-E2E status.

### Execution and results

A fresh temporary snapshot contains 301 current source/regression files. The environment remains macOS
arm64, official Go 1.26.8, offline dependency/toolchain settings and `ROUTER_TEST_ENABLE_SU=false`.
No focus/skip filters were set: all 126 E2E specs were selected. The suite has no ConfigMap specs;
no Kubernetes startup or ConfigMap acceptance is performed.

Actual command (JSON report written to the run's temporary directory):

```bash
go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m \
  -ginkgo.json-report=<validation-dir>/ginkgo-report.json
```

| Check | Result |
| --- | --- |
| Complete post-fix E2E | Exit 0; 1,621.20 seconds overall, 1,613.334 seconds in the suite; random seed 1793704385 |
| Spec results | 123 passed, 0 failed, 0 pending, 3 skipped; all skips are existing privileged iptables network specs |
| Builds and teardown | route-server, mock_vllm and config-tool rebuilt successfully; SynchronizedAfterSuite and JSON report generation passed |
| Cleanup and scope | No run-owned processes remain; all 96 checked test ports have no listener; the dedicated runtime temporary directory was cleaned, with logs/reports retained |
| Working-tree effects | All 345 initial files, HEAD and index stayed unchanged during testing; only this bilingual progress update follows the run |

Existing V-08 non-root cleanup still attempts sudo iptables and ignores errors. This behavior is unchanged;
skips and cleanup errors are not firewall coverage. The existing 21 vet copylock diagnostics were neither
changed nor rerun and are not claimed as passing. This is not native Linux, production TLS or real-model acceptance.

### Commit scope and evidence

The commit is limited to 23 files: deployment error handling, the HTTPS-off shared template/help,
build/deployment regressions, Go version alignment, bilingual public guides and this bilingual audit/progress.
The packaging fixture follows the existing upstream resource layout without changing ConfigMap implementation
or manifests. Other existing work, including `.gitignore`, old module-migration progress, skill designs,
internal drafts, images and editor state, is excluded.

Use the Conventional Commits subject `fix(deploy): fail fast and align deployment guidance` and a DCO
`Signed-off-by` from the current repository identity. Local `/private/tmp/routeserver-e2e-precommit.json`
indexes the complete log, JSON report, source-preservation and cleanup checks. This phase authorizes only
a local commit, no push or history rewriting.

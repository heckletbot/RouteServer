# Go module path migration plan

[English](plan.md) | [简体中文](plan.zh-CN.md)

Status: **implemented; local commit authorized on 2026-10-31, no push**. Baseline: `f7e8185`.
The verified Makefile/package fixes are committed locally at that baseline and have not been pushed.

## 1. Goal and boundaries

Change the main module identity from `infer-dp-scheduler` to the requested `route-server`, with its
internal imports, build entry strings and linker targets updated in one atomic change.

Keep source directories and package declarations such as `cmd/route_server`, `pkg/route-server` and
`package route_server`. Keep the binary/package layout already fixed to `route-server`. HTTP routes,
JSON/YAML fields, environment variables, metrics, error codes, OTEL service identity, product name and
copyright headers are outside this migration. Old installation-directory upgrades are a separate task.

`route-server` remains a self-contained, non-URL module path. It does not become a network-resolvable
`go get` address. The Go reference distinguishes these paths from downloadable dependency paths,
whose first element needs a dot. [Go module paths](https://go.dev/ref/mod#module-path)

## 2. Confirmed impact inventory

A tracked-source scan at `f7e8185` found **694 occurrences in 162 distinct files**. An import-declaration
scan accounts for 678 of them; the remaining 16 are listed below. Counts exclude untracked audit/plan
documents. Recompute before implementation if the branch advances.

| Area | Current scope | Required change |
| --- | --- | --- |
| Go imports | 678 declarations in 157 files: 15 under `cmd/`, 122 under `pkg/`, 20 under `e2etest/`; 101 production files and 56 test files | Replace only the old module-prefix segment in import paths; preserve aliases, declarations and implementation |
| [go.mod](../../../go.mod) | One module directive | `module route-server`; keep the Go version and dependency versions |
| [E2E suite bootstrap](../../../e2etest/route-server/rs_suit_test.go) | Three `gexec.Build` arguments, in a file already counted among the 157 | Use `route-server/cmd/route_server`, `route-server/cmd/mock_server`, `route-server/cmd/config-tool` |
| [Makefile](../../../Makefile) | Four `-X` symbols | Change the package prefix of `version`, `tagOrBranch`, `commitId`, `buildTime` |
| [CodeArts build](../../../.codearts/build.sh) | The same four `-X` symbols | Apply the identical package-prefix change |
| [Contribution guide](../../../CONTRIBUTING.md) and [Chinese guide](../../../CONTRIBUTING.zh-CN.md) | Four occurrences in module/import examples | Describe the new active module path |

There is one tracked `go.mod`, no tracked `go.work` or vendor tree, and no existing module-alias
`replace` directive. Newly added validation code and release notes are additional to this inventory.

Linker targets become `route-server/cmd/route_server/server.<variable>`. Go's `-X` flag addresses a
string variable by import path and name, so the package prefix must match the migrated code.
[Go linker documentation](https://pkg.go.dev/cmd/link)

## 3. Compatibility decisions before implementation

- Treat this as a source-level breaking change for consumers importing the old prefix. Check internal
  repositories, forks, local `replace`/workspace users and platform build jobs. Their existence or
  absence cannot be established from this checkout alone; record the owner and result of that check.
- Recommended default: migrate this repository atomically and coordinate downstream import changes,
  without a compatibility module. Do not leave old imports behind using an automatic self-replacement.
  If a downstream must keep the old import path, stop and obtain approval for a separate compatibility
  design before implementation.
- `replace` is a module-resolution control in a main module, not a general source-import rewrite or a
  downstream-wide alias. A dependency's replacement directives do not configure its consumers.
  [Go replacement rules](https://go.dev/ref/mod#go-mod-file-replace)
- Existing binaries are not rewritten. New stack traces, symbols and profiles may expose the new
  package paths; check any path-based tooling. Public runtime protocols and configured identities are
  expected to stay stable and must be checked explicitly.
- Arrange access to the existing Linux amd64/arm64 build runners and required dependency caches.
  This plan does not assert that those platform checks or downstream migrations are already complete.

## 4. Proposed execution sequence

1. **Baseline and approval gate.** Obtain explicit permission to implement; settle downstream handling
   above. Record the exact revision, Go/Make/Bash versions, package graph and baseline test results.
   Use the declared Go 1.25 toolchain and the current CI toolchain as separate recorded environments;
   do not change the module's Go requirement as part of the rename.
2. **Atomic source migration.** The future implementation starts with `go mod edit -module route-server`.
   Use a Go-AST-based edit of `ImportSpec.Value` for paths equal to `infer-dp-scheduler` or beginning
   `infer-dp-scheduler/`; format changed Go files. Update the three E2E build strings and eight linker
   symbol targets in the same working change. Do not rename directories or package identifiers.
3. **Guides and regression coverage.** Update both contribution guides and add a bilingual release
   note for the agreed next version. Include downstream import instructions and the breaking-change
   notice. Extend validation of actual linker targets and metadata; the existing mocked archive test
   alone cannot prove linker injection works. Preserve the Make failure-regression checks.
4. **Validation gate.** Run the matrix below and compare it to the baseline. Inspect `go mod tidy -diff`
   rather than automatically accepting dependency churn; investigate any dependency or checksum change.
   Stop on unexpected protocol/configuration changes, mixed old/new package paths or unresolved
   downstream compatibility.
5. **Review and delivery.** Produce the exact diff, commands, results and remaining risks in the
   progress record. Keep the migrated source uncommitted until the user authorizes that commit;
   pushing is a separate action. A possible later title is
   `fix(module)!: align module path with route-server`, with a `BREAKING CHANGE:` footer describing
   import migration. Follow the existing two-reviewer/area-owner requirements.

This is the approved execution sequence. Repository migration is now authorized; actual results and
remaining external gates are recorded in [progress.md](progress.md). The user subsequently authorized
a local commit; pushing remains unauthorized.

## 5. Validation matrix and acceptance

The commands below define validation requirements. Executed commands and outcomes are recorded in
the progress record; this matrix is not a claim that every check passed. Run destructive build cleanup
only in a disposable validation checkout.

| Gate | Proposed check | Acceptance |
| --- | --- | --- |
| Module identity | `go list -m -f '{{.Path}}'`; inspect `go.mod` | Exactly `route-server`; no dependency/Go-version changes |
| Active-path completeness | AST scan of Go imports; scan E2E build strings and Make/CodeArts linker targets | No old active prefix; historical audits and migration explanations may retain it |
| Package/dependency graph | `go list -mod=readonly ./...` and `go list -mod=readonly -deps -test ./...` | Same repository package set after prefix normalization; no mixed old/new identity or unexpected dependency changes |
| Formatting and compile/link | Changed-file `gofmt -l`, `git diff --check`, `go test -mod=readonly ./... -run '^$' -count=1` | No formatting errors; every package and test binary loads and links |
| Go regression | `go test -mod=readonly ./cmd/... ./pkg/... -count=1 -timeout 60m`; `go vet -mod=readonly ./...` | Pass under the recorded baseline toolchain/settings |
| Race coverage | `go test -mod=readonly -race ./cmd/... ./pkg/... -count=1 -timeout 60m` | Pass on a supported native runner; record runner/toolchain and any pre-existing failures |
| E2E bootstrap and behaviour | `ROUTER_TEST_ENABLE_SU=false go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m` | All three migrated `gexec.Build` entries work; normal non-root suite passes; privileged iptables cases recorded as skipped |
| Existing build regressions | `python3 .codearts/test_makefile.py` | Existing Go/tee failure propagation, arguments and archive layouts still pass |
| Real producers | Fresh `make package` and native Linux CodeArts builds on amd64/arm64 | Real binaries and archives have the existing `route-server` layout; no mocked-build result substituted |
| Runtime/version metadata | The real-artifact procedure below | Four injected fields match build inputs, and stable identities/protocol fields remain unchanged |
| Dependency stability | `go mod tidy -diff`, inspect `go.mod`/`go.sum` | No unexplained graph/checksum changes; no introduced self-alias |

`-short` does not currently exclude project E2E; explicit `./cmd/... ./pkg/...` is used for the unit/race
stages. Tests use gomonkey, so establish their baseline behaviour on the chosen compiler/architecture.
If baseline tests fail or need special compiler flags, record the exact failure/settings and seek a
separate fix where necessary; do not turn a blocked check into a pass by silently skipping it.

### Real-artifact version check

- Build from an empty output directory in a disposable checkout. For Make, supply distinct sentinel
  values for `VERSION`, `BRANCH`, `COMMIT` and `BUILD_TIME` to the actual build target.
- For CodeArts, build on a native Linux runner and record the inputs derived from its checkout/time;
  compare against `version.info`, the artifact version and the recorded build log.
- Start each actual artifact using the existing local E2E fixture on loopback, then request the
  existing `GET /` endpoint. Assert `version`, `tag_or_branch`, `commit_id` and `build_time` equal the
  actual build inputs. Do not accept default `Unknown` values or mere binary/ZIP existence as proof.
- Add focused test coverage around the existing `printVersion` response if needed. It must exercise
  injected values, not assign the expected values directly to the production variables inside a test.
  No production test-only endpoint or new CLI switch is proposed.
- Inspect real binary build information with `go version -m`; confirm the new module path. Shut down
  temporary servers and release their ports after the check. Compare API/config keys, error prefixes,
  metric names and trace identity with baseline fixtures.

## 6. Acceptance, rollback and current status

Migration is acceptable only after all coupled paths are updated, the validation matrix passes or
explicitly recorded pre-existing blocks are resolved, and downstream handling is confirmed. Runtime
identity remains `RouteServer`, package/binary naming remains `route-server`, and Go identifier syntax
such as `route_server` remains unchanged.

If future implementation fails before commit, discard only its isolated migration change after
preserving unrelated work. If a later authorized commit needs rollback, revert that dedicated commit
and rerun the same gates; do not reset shared history or restore the old deployment-directory name.
Old-installation migration is not solved by rolling back a module prefix.

Current result: the repository module, imports, E2E entries and linker prefixes have been migrated in
the working tree. Dependency versions and runtime protocols remain unchanged. Baseline test failures,
race reports and native Linux validation limits are tracked in [progress.md](progress.md); this is not
an all-green release acceptance. The earlier [naming audit](../documentation-review/implementation-audit.md)
retains its historical baseline. The user authorized a dedicated local commit of this implementation
and its supporting records; no push or release is authorized.

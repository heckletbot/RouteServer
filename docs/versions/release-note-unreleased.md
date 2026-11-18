# Unreleased changes

[English](release-note-unreleased.md) | [简体中文](release-note-unreleased.zh-CN.md)

This records source changes for a future release; it does not announce a published version.

## Breaking source change: Go module path

The Go module path changes from `infer-dp-scheduler` to `route-server`. Consumers that import project
packages must change the module-prefix segment in their Go imports and coordinate any local
`require`/`replace` or workspace configuration. For example:

```go
// Before
import "infer-dp-scheduler/pkg/common/config"
// After
import "route-server/pkg/common/config"
```

The new name remains a self-contained, non-URL module path; it is not a public network download
address. There is no old-path compatibility module or self-replacement alias. Existing downstream
consumers have not been inventoried outside this repository.

Source directories, Go package declarations, HTTP routes, configuration keys, environment variables,
metrics, error prefixes and OTEL service identity are preserved. The package/executable directory
remains `route-server`; upgrading old installation directories is a separate operation.

Both build producers update all four linker version targets, and the E2E bootstrap updates its three
Go build entry paths. Real-artifact metadata smoke tests cover the existing `GET /` response.

## Validation status

Actual commands, baseline failures and platform limits are recorded in the
[module migration progress](../features/module-path-migration/progress.md). A local migration commit
is authorized; no push or release is authorized. This note does not imply that every release-platform
gate passed.

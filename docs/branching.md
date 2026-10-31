# Branching and Release Model

[English](branching.md) | [简体中文](branching.zh-CN.md)

This document is the authoritative description of how code moves in the public
`InferTurbo/RouteServer` repository: which branches exist, how changes are merged, and how a release
is cut. It describes the steady state; the internal export pipeline and one-off migration steps are
operations runbooks, not part of this policy.

Related: [CONTRIBUTING.md](../CONTRIBUTING.md) · [GOVERNANCE.md](../GOVERNANCE.md)

## 1. Model

RouteServer uses **trunk-based development with a single protected trunk**. `main` is the only
long-lived development branch and the only release source. Topic branches are short-lived and deleted
on merge.

```
 topic branch (fork or in-repo) ──pull request──▶ main (protected trunk, release source)
                                                  │
                                                tag vX.Y.Z
```

## 2. Branches

| Branch | Written by | Merges into `main`? | Lifetime | Notes |
| --- | --- | --- | --- | --- |
| `main` | committers or maintainers, only via pull request | — | permanent | Protected. The only release source. |
| `intranet-sync` | the release pipeline, only | yes, via a reviewable import pull request | permanent | **Read-only.** Machine-written and force-pushed; see §5. |
| `feat/*`, `fix/*`, `docs/*`, `refactor/*`, `chore/*` | one contributor | yes, squash merge | < 1 week | Deleted on merge. May live in a fork. |
| tags `vX.Y.Z` | maintainers | — | permanent | Annotated. Created on `main` only. |

## 3. Merge rules

| Change | Method | Why |
| --- | --- | --- |
| Contributor pull request | **Squash merge** | One commit per logical change keeps `main` linear. |
| Import pull request (`intranet-sync` → `main`) | **Merge commit** (`--no-ff`) | Keeps the imported history intact and marks where internal history ends. |
| Hotfix | Squash merge into `main`, then tag a release from `main` | Uses the same release source and gates as other changes. |

Hard rules:

- **No direct pushes to `main`**, including for maintainers; every change arrives as a pull request.
- **No force-push on `main`, ever.** Only the release pipeline force-pushes `intranet-sync`.
- **Never target `intranet-sync` with a pull request.** It is a mirror; merges there are overwritten by
  the next import. Such pull requests are closed with a pointer to `main`.
- **No merge commits inside a topic branch.** Rebase onto `main` before asking for review.

## 4. Review and CI gates

The following requirements are to be configured in the GitCode branch protection settings
([docs](https://docs.atomgit.com/en/docs/help/home/org_project/project_manage/branch_management/set_protected_branch/)):

- Prohibit direct pushes — including for administrators.
- Require a pull request with approvals per [GOVERNANCE.md §5](../GOVERNANCE.md#5-review-and-merge-requirements):
  one for ordinary documentation-only or test-only changes, two for code, sensitive changes and policy changes.
  Required owner or maintainer approvals are included in that total; never count the author. Mixed
  changes take the stricter requirement, and governance policy changes also require the §10 vote.
- Require all review conversations to be resolved before merging.
- Require the pipeline to pass.
- Allow committers and maintainers to merge only after all required approvals and checks pass;
  neither role, including administrators, bypasses the gates. Repository settings, role management
  and releases remain maintainer responsibilities.
- Disable force-push and branch deletion.

Required checks, in the order a contributor hits them:

| Check | Command it maps to |
| --- | --- |
| `build` | `make build` |
| `lint` | `make lint` (gofmt + go vet) |
| `unit-test` | `make test-unit` |
| `e2e` | `go test ./e2etest/route-server/ -timeout 60m` (may run nightly on slow runners) |
| `dco` | every commit carries `Signed-off-by` |

Configuration status: this repository contains `.codearts/build.sh`, but no configuration for the
complete set of checks above. Platform-side CI, required checks, branch protection and CODEOWNERS team
permissions have not been verified. This list states the intended gates, not evidence that they are
already enforced. PR title and commit message formats also require review or a separately configured
validator; the PR template does not validate them.

Verify that the platform can enforce the change-dependent approval counts and owner requirements
before configuring those rules. An unsupported setting must not be treated as automatic enforcement.

The current Make test targets do not preserve `go test` failure exit codes, and `make test-unit` still
runs the E2E suite. A CI job must use the actual `go test` result rather than treating the Make exit
status as proof of success; see [CONTRIBUTING.md §3](../CONTRIBUTING.md#3-build-and-test).

Area-specific review requirements come from [.github/CODEOWNERS](../.github/CODEOWNERS), and the
general requirements are in
[GOVERNANCE.md §5](../GOVERNANCE.md#5-review-and-merge-requirements).

## 5. The import branch and backflow

`intranet-sync` is written by the release pipeline, not by people. The pipeline force-pushes a snapshot
of the internal repository, so the branch is read-only: a local branch tracking it diverges on every
import, and commits pushed to it are discarded by the next one.

Imports land on `main` as a reviewed pull request merged with a merge commit (`--no-ff`), never
squashed. Before such a pull request is opened, **the internal snapshot must already contain every
commit on `main`**; otherwise the import silently reverts external contributions.

That is the backflow rule: fixes made on `main` are merged back into the internal repository before the
next import. It applies to hotfixes as well — an external fix that never flows back survives at most
until the following import.

## 6. Releases

```bash
# from an up-to-date main
git switch main && git pull --ff-only
git tag -a v0.2.0 -m "RouteServer v0.2.0"
git push origin v0.2.0
```

- Tags are `vMAJOR.MINOR.PATCH`, optionally with `-alpha.N`, `-beta.N` or `-rc.N` (for example
  `v0.1.0-alpha.1`, already in the repository).
- **`main` must be green on the tagged commit**; a tag on a red commit is deleted.
- The release note `docs/versions/release-note-<version>.md` must be merged *before* the tag.
- Pre-1.0, `MINOR` may contain breaking changes; from 1.0 on, a breaking change requires a major bump.
- Only maintainers push tags.

## 7. Release branches and hotfixes

At the current pre-1.0 stage, do not create maintenance release branches such as `release/vX.Y`.
Only the latest release and `main` receive fixes, as described in [SECURITY.md](../SECURITY.md).
If supporting multiple versions becomes necessary, obtain explicit approval and update this document,
[GOVERNANCE.md](../GOVERNANCE.md) and the security support policy together before creating a maintenance
branch. The existence of a branch alone does not establish a support commitment.

Hotfixes:

1. Branch `fix/<slug>` from the latest `main`.
2. Meet the ordinary review and CI gates in [GOVERNANCE.md §5](../GOVERNANCE.md#5-review-and-merge-requirements);
   a committer or maintainer squash-merges the pull request into `main`.
3. Merge the release note before tagging; a maintainer tags and publishes the patch release from the
   green commit on `main`.

During an active incident, label the pull request `hotfix` so it can be prioritized and audited.
The label does not reduce the required approvals or bypass CI; owner requirements still apply.

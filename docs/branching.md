# Branching and Release Model

[English](branching.md) | [简体中文](branching.zh-CN.md)

This document is the authoritative description of how code moves in the public
`InferTurbo/RouteServer` repository: which branches exist, how changes are merged, and how a release
is cut. It describes the steady state; the internal export pipeline and one-off migration steps are
operations runbooks, not part of this policy.

Related: [CONTRIBUTING.md](../CONTRIBUTING.md) · [GOVERNANCE.md](../GOVERNANCE.md)

## 1. Model

RouteServer uses **trunk-based development with a single protected trunk**. `main` is the only
long-lived branch and the only release source. Topic branches are short-lived and deleted on merge.

```
 topic branch (fork or in-repo) ──pull request──▶ main (protected trunk, release source)
                                                  │
                                                tag vX.Y.Z
```

## 2. Branches

| Branch | Written by | Merges into `main`? | Lifetime | Notes |
| --- | --- | --- | --- | --- |
| `main` | maintainers, only via pull request | — | permanent | Protected. The only release source. |
| `intranet-sync` | the release pipeline, only | yes, via a reviewable import pull request | permanent | **Read-only.** Machine-written and force-pushed; see §5. |
| `feat/*`, `fix/*`, `docs/*`, `refactor/*`, `chore/*` | one contributor | yes, squash merge | < 1 week | Deleted on merge. May live in a fork. |
| `release/vX.Y` | release manager | no — it receives cherry-picks | until that minor version reaches EOL | Only created when two minor versions must be maintained at once. |
| tags `vX.Y.Z` | maintainers | — | permanent | Annotated. Created on `main` only. |

## 3. Merge rules

| Change | Method | Why |
| --- | --- | --- |
| Contributor pull request | **Squash merge** | One commit per logical change keeps `main` linear. |
| Import pull request (`intranet-sync` → `main`) | **Merge commit** (`--no-ff`) | Keeps the imported history intact and marks where internal history ends. |
| Hotfix | Squash merge, then `cherry-pick -x` to `release/vX.Y` | Keeps the fix identical in both places. |
| Release branch → `main` | never | Releases flow the other way: `main` → tag. |

Hard rules:

- **No direct pushes to `main`**, including for maintainers; every change arrives as a pull request.
- **No force-push on `main`, ever.** Only the release pipeline force-pushes `intranet-sync`.
- **Never target `intranet-sync` with a pull request.** It is a mirror; merges there are overwritten by
  the next import. Such pull requests are closed with a pointer to `main`.
- **No merge commits inside a topic branch.** Rebase onto `main` before asking for review.

## 4. Review and CI gates

Configured in the GitCode branch protection settings
([docs](https://docs.atomgit.com/en/docs/help/home/org_project/project_manage/branch_management/set_protected_branch/)):

- Prohibit direct pushes — including for administrators.
- Require a pull request with **at least 2 approvals**, and never count the author.
- Require all review conversations to be resolved before merging.
- Require the pipeline to pass.
- Restrict merge permission to the `maintainer` role.
- Disable force-push and branch deletion.

Required checks, in the order a contributor hits them:

| Check | Command it maps to |
| --- | --- |
| `build` | `make build` |
| `lint` | `make lint` (gofmt + go vet) |
| `unit-test` | `make test-unit` |
| `e2e` | `go test ./e2etest/route-server/ -timeout 60m` (may run nightly on slow runners) |
| `dco` | every commit carries `Signed-off-by` |

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

Create `release/vX.Y` only when the previous minor version must keep receiving fixes while `main`
moves on:

```bash
git switch -c release/v0.1 v0.1.3
# fixes: cherry-pick -x <sha> from main, never merge main in
```

Hotfixes:

1. Branch `fix/<slug>` from `main` (not from the release branch).
2. Ordinary review, ordinarily merged.
3. Tag a patch release from `main`.
4. If a maintained `release/vX.Y` is affected, `git cherry-pick -x <merge-sha>` and tag there too.

Emergency path: during an active incident a single maintainer may merge with one approval, provided the
pull request is labelled `hotfix` and a second review is completed within 24 hours. The label exists so
these cases are auditable.

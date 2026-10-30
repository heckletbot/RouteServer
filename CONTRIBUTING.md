# Contributing to RouteServer

Thanks for your interest in RouteServer. This document describes how to propose a change, how to get it
reviewed and merged, and what we expect from a contribution. It applies to everyone — external
contributors, committers and maintainers alike.

- 中文版：[CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)
- Branch model and merge rules: [docs/branching.md](docs/branching.md)
- Roles, promotion and decision making: [GOVERNANCE.md](GOVERNANCE.md)
- Reporting a vulnerability: [SECURITY.md](SECURITY.md) (never via a public issue)

## Contents

1. [Before you start](#1-before-you-start)
2. [Development environment](#2-development-environment)
3. [Build and test](#3-build-and-test)
4. [Branching and pull requests](#4-branching-and-pull-requests)
5. [Commit messages](#5-commit-messages)
6. [Developer Certificate of Origin](#6-developer-certificate-of-origin)
7. [Coding guidelines](#7-coding-guidelines)
8. [Documentation](#8-documentation)
9. [What we cannot accept](#9-what-we-cannot-accept)
10. [Review process](#10-review-process)
11. [License](#11-license)

## 1. Before you start

- Read [docs/architecture.md](docs/architecture.md) and [docs/code-structure.md](docs/code-structure.md)
  first. Most review comments come from not knowing which package owns which responsibility.
- **Bug fix or small improvement** (a few hundred lines): open the pull request directly.
- **Anything larger** — a new scheduling policy, a new orchestration mode, an API/protocol change, a new
  dependency, a new top-level directory — open an issue describing the problem and the proposed design
  and wait for a maintainer to agree on the direction. This avoids you writing a large patch that we
  cannot merge.
- Search existing issues and pull requests before opening a new one.
- **Do not report security vulnerabilities in a public issue.** See [SECURITY.md](SECURITY.md).

## 2. Development environment

| Tool | Minimum version | Notes |
| --- | --- | --- |
| Go | 1.25.0 | The module declares `go 1.25.0` |
| Make | 3.82 | Drives build, test and packaging |
| Git | 2.23 | Needed for `--force-with-lease` and worktrees |

The Go module path is `infer-dp-scheduler` (it is not a URL). Internal imports therefore look like
`infer-dp-scheduler/pkg/common/scheduler`.

```bash
# Fork InferTurbo/RouteServer on GitCode, then:
git clone git@gitcode.com:<your-user>/RouteServer.git
cd RouteServer
git remote add upstream git@gitcode.com:InferTurbo/RouteServer.git
git fetch upstream
git switch -c feat/my-change upstream/main
```

## 3. Build and test

```bash
make build         # build outputs, see the table below
make test          # go test ./... -v, log written to build/test.log
make test-unit     # go test ./... -short, skips slow integration paths
make lint          # go fmt + go vet
make package       # build + deploy assets + version.info + zip
```

Build artifacts (these names come from the `Makefile`, which is authoritative):

| Target | Output |
| --- | --- |
| `make build` | `output/DistributedScheduler/bin/route-server`, `output/config_generator` |
| `make package` | `output/DistributedScheduler-<version>-<timestamp>.zip` |

> Note: `README.md` and `docs/build.md` still mention older package names (`ModelOrch-*`,
> `route-server-*`). Trust the `Makefile` output above.

### End-to-end tests

The end-to-end suite builds the server, the mock vLLM server and the config tool itself, then runs them
as real processes. It needs free ports in the `30xxx` range and does not require an external etcd (the
suite embeds one).

```bash
go test ./e2etest/route-server/ -v -timeout 60m
```

Some cases manipulate `iptables` and are skipped by default. To run them you need root and:

```bash
ROUTER_TEST_ENABLE_SU=true sudo -E go test ./e2etest/route-server/ -v -timeout 60m
```

On Windows a handful of process-kill tests are skipped. If your change touches the data path, say in the
pull request which of these suites you ran.

### Before you push

```bash
make lint && make test
```

Both must be clean. A pull request whose CI is red is not reviewed.

## 4. Branching and pull requests

The repository uses trunk-based development with a single protected trunk, `main`. Details and the
required checks are in [docs/branching.md](docs/branching.md).

1. Branch from the latest `upstream/main`, not from a release tag or an old fork.
2. Use a short-lived branch: `feat/<slug>`, `fix/<slug>`, `docs/<slug>`, `refactor/<slug>`,
   `chore/<slug>`. Keep it under a week of work.
3. Keep one logical change per pull request. If you find an unrelated bug, send a second pull request.
4. Push to **your fork** and open the pull request against `InferTurbo/RouteServer` `main`.
5. Fill in the pull request template — including how you tested it.
6. Wait for CI, then for review. Address comments by pushing new commits; maintainers squash on merge.

**Never target `intranet-sync`.** It is not a development branch: it is the machine-written mirror of
our internal repository, and anything pushed or merged into it is overwritten on the next sync. Pull
requests against `intranet-sync` are closed without review, with a pointer to `main`.

Committers with write access may push topic branches into the repository itself instead of a fork; the
review and merge rules are identical.

## 5. Commit messages

We follow [Conventional Commits](https://www.conventionalcommits.org/). The subject line is
`<type>(<scope>): <imperative summary>`, at most 72 characters, no trailing period.

| Type | Use for |
| --- | --- |
| `feat` | A user-visible capability |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `refactor` | No behaviour change |
| `test` | Tests only |
| `perf` | Measurable performance improvement |
| `build` | Build system, Makefile, packaging |
| `ci` | Pipelines and automation |
| `chore` | Everything else (deps, housekeeping) |

Examples:

```
feat(scheduler): add least-requests policy
fix(etcd): retry lease renewal on transient errors
docs(deploy): correct default log file path
```

- Scope is optional but strongly preferred; use the package or subsystem name (`scheduler`, `etcd`,
  `handler`, `metrics`, `deploy`).
- Reference the issue in the body (`Refs #123`, `Fixes #123`), not in the subject.
- A breaking change must be marked `feat!:` or `fix!:` **and** carry a `BREAKING CHANGE:` footer
  explaining the migration. Breaking changes need an agreed issue first.

## 6. Developer Certificate of Origin

We use the DCO, not a CLA. Every commit must be signed off:

```bash
git commit -s
```

which appends:

```
Signed-off-by: Your Name <you@example.com>
```

The name and e-mail must match your `git config user.name` / `user.email` and must be an address you
can be reached at. By signing off you certify the [DCO 1.1](https://developercertificate.org/): that you
wrote the change or have the right to submit it under the project licence.

**Prefer a GitCode noreply address** — `<gitcode-username>@noreply.gitcode.com` — over a corporate
mailbox: it keeps internal naming out of a public repository. The project's [`.mailmap`](.mailmap)
normalizes the identities that reached the history before this rule existed. It changes display only —
history and SHAs are never rewritten.

A pull request with unsigned commits will fail the DCO check. To fix an existing branch:

```bash
git rebase --signoff upstream/main
git push --force-with-lease
```

Signing commits with GPG/SSH is optional but recommended.

## 7. Coding guidelines

- `gofmt` and `go vet` must be clean; CI enforces both via `make lint`.
- Every new `.go` file starts with the project licence header (copy it from any existing file):

  ```go
  /*
   * Copyright <year> The RouteServer Authors.
   *
   * Licensed under the Apache License, Version 2.0 (the "License");
   * you may not use this file except in compliance with the License.
   * You may obtain a copy of the License at
   *
   *     http://www.apache.org/licenses/LICENSE-2.0
   *
   * Unless required by applicable law or agreed to in writing, software
   * distributed under the License is distributed on an "AS IS" BASIS,
   * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   * See the License for the specific language governing permissions and
   * limitations under the License.
   */
  ```

- Wrap errors with `%w` and enough context to identify the call site; do not discard errors silently.
- No `panic` on a request path. Return an error and let the caller decide.
- Every exported symbol gets a doc comment starting with its name.
- Concurrency: document the goroutine that owns a mutable field; prefer passing context explicitly over
  storing it.
- Tests are part of the change. New logic needs unit tests; data-path changes should extend the
  end-to-end suite. Table-driven tests are the house style for pure functions.
- A **new third-party dependency** needs maintainer approval in the pull request: it must be
  Apache-2.0-compatible, actively maintained, and justified against the standard library or an existing
  dependency. Run `go mod tidy` and commit the resulting `go.mod`/`go.sum` changes.
- Do not commit binaries, archives, generated blobs, or editor state. `build/`, `output/`,
  `.DS_Store`, `.idea/` and `.vscode/` are ignored — keep it that way.

## 8. Documentation

- User-visible behaviour changes update the corresponding file under `docs/` **in the same pull
  request**. Configuration changes update [docs/config-spec.md](docs/config-spec.md); API changes update
  [docs/restful-api.md](docs/restful-api.md); deployment changes update [docs/deploy.md](docs/deploy.md).
- A release-worthy feature also adds an entry under `docs/versions/release-note-<version>.md`.
- Diagrams live in `docs/imgs/`; prefer plain Markdown and fenced code blocks over external links that
  can rot.

## 9. What we cannot accept

These are closed without review, and repeated instances may lead to a block:

- **Internal-only information**: internal hostnames or domains, employee IDs, internal ticket or
  document links, internal build machine paths, non-public repository URLs, unredacted log dumps.
  Before pushing, check your diff:

  ```bash
  git diff upstream/main... -- . ':!/.mailmap' \
    | grep -nEi 'huawei|codehub|3ms\.com|/home/[a-z]+|10\.[0-9]+\.[0-9]+\.[0-9]+' || true
  ```

  Matches are not automatically wrong (private-range addresses appear in deployment examples), but they
  must be intentional and use documentation placeholders.
- Credentials of any kind: keys, tokens, certificates, kubeconfigs, `.env` files with real values.
- Code copied from a project whose licence is incompatible with Apache-2.0, or with its copyright
  headers stripped.
- Unrelated mass reformatting, renaming or file moves mixed into a functional change.
- A pull request that bundles several independent features.

## 10. Review process

- A maintainer or committer gives a first response within **7 calendar days**. If nobody has responded
  by then, comment on the pull request with `@InferTurbo/maintainers` — that is a process failure on our
  side, not yours.
- Two approvals are required, from people other than the author. Changes under `pkg/route-server/scheduler/`,
  `pkg/common/scheduler/`, `pkg/route-server/middleware/`, `pkg/common/http/` and `deploy/` additionally
  require an owner of that area — see [.github/CODEOWNERS](.github/CODEOWNERS).
- Reviewers may ask for a design change. Push new commits rather than rewriting history during review;
  do not force-push a branch under active review unless you are asked to.
- When approved and green, a maintainer squash-merges and deletes the branch. You do not need to merge
  it yourself.
- A pull request with no activity for 30 days, and no response to a ping, is closed as stale. It can be
  reopened.

## 11. License

RouteServer is licensed under the [Apache License 2.0](LICENSE). By submitting a pull request you agree
that your contribution is licensed under the same terms and that you have the right to submit it (see
[section 6](#6-developer-certificate-of-origin)).

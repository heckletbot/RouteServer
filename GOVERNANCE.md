# RouteServer Governance

This document defines who can do what in the RouteServer project, how people gain those rights, and how
disagreements are resolved. It is intentionally short: the goal is that any contributor can predict how a
decision will be made before making it.

- 中文版：[GOVERNANCE.zh-CN.md](GOVERNANCE.zh-CN.md)
- Contribution mechanics: [CONTRIBUTING.md](CONTRIBUTING.md)
- Branch model and merge rules: [docs/branching.md](docs/branching.md)
- Security reporting: [SECURITY.md](SECURITY.md)
- Code of conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

## 1. Roles

| Role | What they can do | How you get it |
| --- | --- | --- |
| **Contributor** | Open issues, send pull requests, take part in design discussion | Anyone, after signing off commits (DCO) |
| **Reviewer** | Review and approve pull requests. Cannot merge | Named by a maintainer after sustained review work |
| **Committer** | Approve and merge pull requests, push topic branches into the repository, triage issues and labels | Nominated and approved (see §2) |
| **Maintainer** | Everything a committer does, plus: change branch protection, manage repository settings and teams, cut releases, handle security reports, appoint reviewers/committers, amend this document | Promoted from committers (see §3) |

Roles are additive and public. The current list of reviewers, committers and maintainers is kept in
[MAINTAINERS.md](MAINTAINERS.md) and in the repository's team settings; if the two disagree, the team
settings win and the file is a bug.

## 2. Becoming a committer

A committer is expected to be a reliable reviewer as much as a reliable author. The minimum bar:

- **10 or more non-trivial pull requests** merged into `main` (typo fixes and dependency bumps do not
  count), spanning **at least three months** of activity.
- Demonstrated review judgement: at least **5 substantive reviews** on other people's pull requests that
  the author acted on.
- No history of bypassing CI, self-approving, or merging without the required approvals.
- Familiarity with the subsystems they will review, reflected in
  [.github/CODEOWNERS](.github/CODEOWNERS) area ownership.

Process:

1. Any committer nominates the candidate in a private maintainer channel, with links to the evidence
   above.
2. Two committers must second the nomination.
3. Maintainers have **7 days** to raise an objection. Silence is consent (lazy consensus); an unresolved
   objection blocks the promotion and must be explained to the candidate.
4. A maintainer adds the person to the committer team, and the pull request that updates
   `MAINTAINERS.md` records the date.

Sustained contributions from outside the originating organisation are explicitly welcome: a committer
seat is not reserved for employees of any company.

## 3. Becoming a maintainer

- At least **six months** as a committer, including at least one release cycle as release manager or an
  equivalent sustained responsibility.
- Approval by **two thirds of the current maintainers**, in a vote that stays open for 7 days.
- Maintainers should keep the group at **three or more people**, so that no single person's absence can
  block reviews, releases or security response.

## 4. Inactivity and emeritus status

A committer or maintainer who has had no review, commit or triage activity for **six consecutive
months** moves to *emeritus*: their name stays in `MAINTAINERS.md` with an end date, and their write
access is removed. Emeritus status is not a judgement — people change jobs and priorities. Returning is
easy: ask a maintainer, and access is restored without a new vote for the first three months after the
move.

## 5. Review and merge requirements

| Change | Approvals | Additional requirement |
| --- | --- | --- |
| Documentation, comments, typo (no policy change) | 1 | — |
| Tests only | 1 | CI green |
| Ordinary code (`pkg/`, `cmd/`) | 2 | CI green |
| Scheduler, middleware, HTTP/TLS handling | 2 | 1 approval must be from an area owner |
| Build, deployment or CI rules/behaviour (`deploy/`, `Makefile`, `.codearts/`) | 2 | 1 approval from a release owner |
| Project policy or branch protection changes, including `GOVERNANCE.md` and `SECURITY.md` and their translations | 2 | at least 1 maintainer approval |
| Security fix | 1 maintainer + 1 security reviewer | Private branch until disclosure, see [SECURITY.md](SECURITY.md) |

This table is the authoritative source for approval requirements; counts refer to distinct non-author
reviewers. Classify a change by its effect: documentation-only and test-only changes need one approval
unless they change project policy, build,
deployment or CI rules. Mixed changes must satisfy the stricter applicable count and all applicable
owner or maintainer requirements. Required area-owner and maintainer approvals count **within** the
total, not as extra reviews. Changes to governance policy also require the vote in §10; purely
editorial fixes do not change policy.

Committers and maintainers may merge once the required approvals, resolved review conversations and
passing checks are in place. Repository settings, role management and releases remain maintainer
responsibilities. Neither role may bypass the gates.

Rules that hold without exception, including for hotfixes:

- **The author never counts as an approver of their own change**, including maintainers.
- **Nobody pushes directly to `main`.** Every change, including a maintainer's, arrives as a pull
  request. Automated imports from the internal repository also require a reviewed pull request
  (see [docs/branching.md](docs/branching.md)).
- **A red pipeline is never merged.** Administrators do not bypass required approvals or checks.

## 6. Decisions

We default to **lazy consensus**: an issue or pull request proceeds unless someone raises a concrete
technical objection, and objections are resolved by discussion in that thread.

When consensus does not appear:

1. The proposer summarises the options and the trade-offs in the issue.
2. Maintainers vote; a simple majority decides, and the vote stays open for 7 days.
3. Amendments to this document, changes to the licence, and changes to the branch protection model need
   a **two-thirds** majority of maintainers.

Decisions are recorded in the issue that proposed them, with the reasoning — not only the outcome. A
later reader must be able to tell why the alternative was rejected.

**Escalation.** If a pull request or issue is stuck because a reviewer is unresponsive rather than
because of a technical disagreement, any committer may ask a maintainer to reassign the review. Stale
review assignments are a process bug, not a reason to lower the review bar.

## 7. Releases

- At the current pre-1.0 stage, all releases, including hotfix releases, are cut from `main` only.
  Maintenance release branches are not used. Supporting multiple versions requires an explicitly
  approved policy change covering both [docs/branching.md](docs/branching.md) and
  [SECURITY.md](SECURITY.md) before a maintenance branch is created.
- A **release manager** coordinates each release, with the role volunteered in the release tracking
  issue. Maintainers remain responsible for creating release tags and publishing releases.
- Only maintainers create tags, and only `vX.Y.Z` (with an optional `-rc.N` / `-alpha.N` / `-beta.N` pre-release
  suffix) — see [docs/branching.md](docs/branching.md) for the tagging rules.
- Every release has a note in `docs/versions/release-note-<version>.md` listing user-visible changes,
  breaking changes and known issues. The note is part of the release, not an afterthought.
- A release is only published after CI is green on the tagged commit.

## 8. Dependencies and third-party code

Adding a third-party dependency is a governance decision, not just a build detail:

- The dependency must be actively maintained and licensed compatibly with Apache-2.0.
- The pull request that adds it states why the standard library or an existing dependency cannot do the
  job, and a maintainer approves that specific point.
- Vendored copies of external code keep their original licence headers and are listed in `NOTICE` if
  their licence requires it.

## 9. Security

- Reports arrive privately per [SECURITY.md](SECURITY.md) and are visible only to the maintainers plus
  anyone they explicitly bring in.
- A security fix may be developed on a private branch and merged as a single commit once a release
  containing it is ready. This is the only case where the public review trail is not complete at merge
  time; the pull request is published (or a summary is) after disclosure.
- Maintainers must not use their access to inspect non-public vulnerability reports for unrelated
  purposes.

## 10. Amending this document

Changes to this document are ordinary pull requests against `main` with the review requirement in §5,
plus a two-thirds maintainer vote recorded in the pull request. Editorial fixes (typos, broken links)
need only one approval.

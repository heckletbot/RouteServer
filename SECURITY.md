# Security Policy

- 中文版：[SECURITY.zh-CN.md](SECURITY.zh-CN.md)

## Supported versions

RouteServer is pre-1.0. Only the latest release and `main` receive security fixes.

| Version | Supported |
| --- | --- |
| `main` (unreleased) | Yes |
| latest `v0.x` release | Yes |
| older `v0.x` releases | No — upgrade to the latest release |
| `v0.x.y-alpha.N` / `-rc.N` pre-releases | Best effort only; not recommended for production |

## Reporting a vulnerability

**Do not open a public issue, discussion or pull request for a security problem.**

Use one of the following private channels:

1. **Preferred** — GitCode's private security report for this repository: on the repository page choose
   *Security* → *Report a vulnerability*. This creates a private advisory visible only to maintainers.
2. **Direct contact** — if the advisory flow is unavailable, send a direct private message to one of the
   security responders listed in [MAINTAINERS.md](MAINTAINERS.md). Do not include the technical details
   in a public channel.

If you are unsure whether something is a vulnerability, report it privately anyway. We would rather
triage a non-issue than miss a real one.

Please include:

- affected version, commit or tag, and the deployment mode (standalone or distributed);
- a description of the issue and its security impact (what an attacker gains);
- reproduction steps or a proof of concept, including the relevant configuration
  (`TLS_*` variables, ports, etcd setup);
- any known mitigation;
- how you would like to be credited, or that you prefer to stay anonymous.

## What to expect

| Stage | Target |
| --- | --- |
| Acknowledgement of the report | 3 business days |
| Initial triage and severity assessment | 7 business days |
| Fix or documented mitigation for **critical** / **high** | 90 days for critical, 180 days for high |
| Public disclosure | coordinated with the reporter, after a fixed release is available |

We will keep you informed at each stage. If a fix takes longer than the target above, we will say so and
explain why rather than going silent.

## Disclosure policy

- We practise coordinated disclosure. The reporter and the maintainers agree on a publication date; the
  default is the day the fixed release is published.
- Credit is given in the release note and the advisory unless the reporter asks otherwise.
- We may publish an advisory with limited detail while a fix is being deployed to hosted instances.
- If a report is already public elsewhere, we will still fix it quickly, but the private channel is no
  longer useful.

## Scope

**In scope**

- The `route-server` binary and the `config_generator` tool built from this repository.
- Request handling, scheduling, health/fault-detection logic, and the etcd coordination paths under
  `pkg/`.
- TLS/mTLS configuration handling, including the private-key passphrase handling described in
  [docs/security.md](docs/security.md).
- Deployment assets in `deploy/` where a default enables a realistic attack.

**Out of scope**

- Vulnerabilities in vLLM, etcd, Prometheus or other third-party components — report those upstream.
- Misconfiguration that the documentation already warns against, such as binding the business or ops port
  to `0.0.0.0` on an untrusted network, or disabling TLS in production.
- Denial of service caused solely by sending more traffic than the deployment was sized for.
- Issues that require an already-compromised host or etcd cluster.
- Findings from automated scanners with no demonstrated impact.

## Hardening before deployment

RouteServer separates a business port (default `9800`) from an ops port and supports mTLS on both. Read
[docs/security.md](docs/security.md) before exposing any deployment beyond a trusted network; in
particular bind to specific interfaces, do not run the ops port on a public interface, and use mTLS for
downstream vLLM and etcd connections.

## Security-relevant contributions

If your pull request touches `pkg/route-server/middleware/`, `pkg/common/http/`, `pkg/route-server/handler/`
or the TLS handling in `deploy/`, it needs an area-owner approval in addition to the normal two reviews.
See [GOVERNANCE.md §5](GOVERNANCE.md#5-review-and-merge-requirements).

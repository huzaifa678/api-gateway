# Policy as Code — Dockerfile & image guardrails

This service's container build passes three guardrail layers, in CI
([`.github/workflows/security.yml`](../.github/workflows/security.yml)) and
locally ([`scripts/guardrails.sh`](../scripts/guardrails.sh)):

| Layer | Tool | Gate |
|---|---|---|
| **Dockerfile PAC** | OPA/Rego via [`conftest`](https://www.conftest.dev/) (`policy/docker`) | **Hard** — a `deny` blocks |
| **Config + secret scan** | [`checkov`](https://www.checkov.io/) (`dockerfile`, `secrets`) | Baseline / report |
| **Vulnerability scan** | [`trivy`](https://trivy.dev/) (image + fs) | **Hard** on fixable CRITICAL/HIGH |

## Dockerfile policy — [`policy/docker/`](docker/)

Evaluated with conftest's `dockerfile` parser. All rules are in `package main`.

- **`deny`** (blocking): unpinned / `:latest` base image, final `USER root`,
  `ADD <remote-url>`.
- **`warn`** (advisory): no `USER` (runs as root), no `HEALTHCHECK`, base pinned
  by tag rather than digest, `RUN` steps fetching a `latest` artifact,
  `apt-get install` without `--no-install-recommends`, secrets in `ENV`.

`deny` rules are calibrated to pass this repo's Dockerfile(s) today and fail a
real regression (someone bases off `:latest`, or ships a root final stage).
[`docker/main_test.rego`](docker/main_test.rego) unit-tests them (`conftest verify`).

## Image scanning (Trivy)

Trivy is the image scanner. In the build pipeline (`*-ci.yml`) the image is
scanned **before it is pushed to ECR** — scan-before-push, fail-closed on fixable
`CRITICAL`/`HIGH` — so a vulnerable image can never be promoted. On PRs,
`security.yml` additionally runs `trivy fs` (dependency CVEs) and `trivy config`
(Dockerfile/IaC misconfiguration). This is orthogonal to code-quality tooling
(e.g. SonarQube), which scans source, not the shipped image.

## Run locally

```bash
./scripts/guardrails.sh          # conftest + checkov + trivy (skips tools you don't have)
conftest verify -p policy/docker # just the Rego unit tests
```

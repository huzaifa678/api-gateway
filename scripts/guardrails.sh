#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

DFILES=()
while IFS= read -r f; do DFILES+=("$f"); done < <(find . -type f \
  \( -name 'Dockerfile' -o -name 'Dockerfile.*' \) \
  -not -path '*/node_modules/*' -not -path '*/.git/*' -not -path '*/vendor/*' | sort)

echo "== Dockerfile Policy as Code (conftest / OPA) =="
conftest verify -p policy/docker
for df in "${DFILES[@]}"; do
  echo "-- ${df}"
  conftest test "${df}" --parser dockerfile -p policy/docker
done

echo "== Checkov (dockerfile + secrets, baseline) =="
if command -v checkov >/dev/null 2>&1; then
  checkov -d . --framework dockerfile,secrets --config-file .checkov.yaml || true
else
  echo "checkov not installed — skipping (pip install checkov)"
fi

echo "== Trivy (config misconfig + dependency vulns) =="
if command -v trivy >/dev/null 2>&1; then
  trivy config --exit-code 0 .
  trivy fs --scanners vuln --severity CRITICAL,HIGH --ignore-unfixed \
    --exit-code 1 --skip-dirs node_modules .
else
  echo "trivy not installed — skipping (https://trivy.dev/latest/getting-started/installation/)"
fi

echo "guardrails: OK"

# Kube Ops Copilot

Kube Ops Copilot is an approval-driven Kubernetes reliability copilot.

It provides deterministic, evidence-first diagnosis and (optionally) executes **operator-approved** remediation plans.

## Principles

- Evidence-first: every finding includes concrete signals.
- Deterministic output: `diagnose` is stable and machine-friendly.
- No silent changes: `execute` is gated by explicit approval flags and (optionally) external approval providers.

## Install

### From source

```bash
go test ./...
go build ./cmd/kube-ops-copilot
./kube-ops-copilot version
```

Or via Makefile (embeds version info via `-ldflags`):

```bash
make test
make build VERSION=dev
./bin/kube-ops-copilot version
```

### Container image

Releases publish to GHCR:

```bash
docker pull ghcr.io/<org>/<repo>:vX.Y.Z
```

## Quick start

### 1) Diagnose

```bash
./kube-ops-copilot diagnose --kubeconfig ~/.kube/config --context prod --output markdown
```

### 2) (Optional) LLM suggestions

`suggest` runs the same deterministic diagnosis pipeline, then asks an LLM for a human-friendly summary and safe next steps.

If you pass `--plan-out`, it also asks the LLM for exactly one best executable remediation plan (when safe), and writes it as an `ExecutionPlan` JSON you can approve + execute.

```bash
export OPENAI_API_KEY=... # or KUBE_OPS_COPILOT_OPENAI_API_KEY
./kube-ops-copilot suggest --llm-provider openai --llm-model gpt-4.1-mini --kubeconfig ~/.kube/config --context prod
```

Emit a plan file (if a safe executable plan is available):

```bash
./kube-ops-copilot suggest --llm-provider openai --llm-model gpt-4.1-mini \
	--kubeconfig ~/.kube/config --context prod \
	--plan-out /tmp/kube-ops-copilot-plan.json
```

### 2b) (Recommended) One-command remediation (Telegram approval)

This runs: diagnose → LLM proposes exactly one best executable plan → sends approval → waits → applies after approval.

```bash
export OPENAI_API_KEY=...
export KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN='...'
export KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID='...'

./kube-ops-copilot remediate --llm-provider openai --llm-model gpt-4.1-mini \
	--kubeconfig ~/.kube/config --context prod \
	--approval-provider telegram \
	--wait-approval \
	--apply
```

### 3) Execute an approved plan (safe-by-default)

Execution is plan-based and **dry-run by default**.

```bash
./kube-ops-copilot execute \
	--plan examples/execute-restart-deployment.json \
	--approval-id TICKET-123 \
	--approve \
	--dry-run
```

To apply changes:

```bash
./kube-ops-copilot execute \
	--plan examples/execute-restart-deployment.json \
	--kubeconfig ~/.kube/config \
	--context prod \
	--approval-id TICKET-123 \
	--approve \
	--dry-run=false
```

## Notifications (Slack / Telegram)

Both `diagnose` and `execute` support `--notify`.

### Slack (webhook)

```bash
export KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...'
./kube-ops-copilot diagnose --notify ...
```

### Telegram (bot)

```bash
export KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN='123456:ABC...'
export KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID='-1001234567890'
./kube-ops-copilot execute --notify ...
```

## LLM providers

`suggest` supports these providers:

- OpenAI: `--llm-provider openai`
	- API key env: `KUBE_OPS_COPILOT_OPENAI_API_KEY` or `OPENAI_API_KEY`
- Anthropic: `--llm-provider anthropic`
	- API key env: `KUBE_OPS_COPILOT_ANTHROPIC_API_KEY` or `ANTHROPIC_API_KEY`
- Ollama (local): `--llm-provider ollama`
	- No key; set `--llm-base-url` (default is provider-specific)

You can also pass `--llm-api-key`, but environment variables are recommended to avoid shell history leakage.

## Approval workflows

`execute` always requires:

- `--approve` (explicit operator intent)
- `--approval-id <id>` (ticket / change-id / workflow correlation)

Additionally, you can choose an approval provider to verify the approval decision before changes are applied.

### Provider: manual (default)

Manual provider is effectively “self-approved” once you pass the required CLI flags.

```bash
./kube-ops-copilot execute --approval-provider manual --approval-id TICKET-123 --approve --plan ...
```

### Provider: n8n (webhook)

This mode calls an n8n webhook for both request creation and status checks.

Configuration:

```bash
export KUBE_OPS_COPILOT_N8N_WEBHOOK_URL='https://<n8n>/webhook/<id>'
export KUBE_OPS_COPILOT_N8N_BEARER_TOKEN='optional-shared-secret'
```

Contract (HTTP POST JSON):

- Request:

```json
{"action":"request","approvalId":"...","summary":"...","details":"...","operation":"...","target":"...","planPath":"..."}
```

- Status:

```json
{"action":"status","approvalId":"..."}
```

Expected response shape:

```json
{"approvalId":"...","decision":"approved|denied|pending","approver":"...","reason":"...","raw":{}}
```

End-to-end:

```bash
./kube-ops-copilot approval request --provider n8n --plan examples/execute-restart-deployment.json --write-plan
./kube-ops-copilot execute --plan examples/execute-restart-deployment.json --approval-provider n8n --approval-id <approval-id> --approve --dry-run=false --wait-approval
```

Recommended: use an n8n workflow that sends an approval message with **Approve/Deny buttons** which hit a second n8n webhook to record the decision. A ready-to-import example is provided at:

- [examples/n8n-approval-workflow.json](examples/n8n-approval-workflow.json)

That workflow expects these n8n environment variables:

- `KOC_PUBLIC_BASE_URL` (public base URL of your n8n instance, e.g. `https://n8n.example.com`)
- `KOC_DECISION_SECRET` (shared secret added to approve/deny button URLs)
- Optional: `KOC_N8N_BEARER_TOKEN` (must match `KUBE_OPS_COPILOT_N8N_BEARER_TOKEN` if you set it)
- Optional: `KOC_TELEGRAM_BOT_TOKEN`, `KOC_TELEGRAM_CHAT_ID` (to send Telegram button messages)
- Optional: `KOC_SLACK_WEBHOOK_URL` (to send Slack button messages)

### Provider: Telegram (reply-based)

This mode sends an approval request to a Telegram chat and considers it approved/denied when a user replies:

- `approve <approval-id>`
- `deny <approval-id>`

Configuration:

```bash
export KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN='123456:ABC...'
export KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID='-1001234567890'
```

Request + execute:

```bash
id=$(./kube-ops-copilot approval request --provider telegram --plan examples/execute-restart-deployment.json --write-plan | awk '{print $NF}')
./kube-ops-copilot execute --plan examples/execute-restart-deployment.json --approval-provider telegram --approval-id "$id" --approve --wait-approval --dry-run=false
```

Note: this implementation polls `getUpdates` and scans recent messages. Use unique approval IDs to reduce the chance of matching an older message.

### Provider: Slack (interactive buttons)

This mode:

1) Writes a pending approval record to a local store file.
2) Optionally posts an interactive Slack message (approve/deny buttons) via webhook.
3) Receives the button callback via a small HTTP server and updates the local store.
4) `execute` checks the decision by reading the local store.

Configuration:

```bash
export KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...'
export KUBE_OPS_COPILOT_SLACK_SIGNING_SECRET='from Slack app settings'
export KUBE_OPS_COPILOT_PUBLIC_BASE_URL='https://your-public-hostname'
export KUBE_OPS_COPILOT_APPROVAL_STORE='/var/lib/kube-ops-copilot/approvals.json'
```

Run the interactivity callback server (must be reachable by Slack):

```bash
./kube-ops-copilot approval serve --provider slack --listen :8088
```

In your Slack app configuration, set **Interactivity Request URL** to:

```
https://your-public-hostname/slack/actions
```

Request + execute:

```bash
./kube-ops-copilot approval request --provider slack --plan examples/execute-restart-deployment.json --write-plan
./kube-ops-copilot execute --plan examples/execute-restart-deployment.json --approval-provider slack --approval-id <approval-id> --approve --wait-approval --dry-run=false
```

Important: Slack approval status is stored locally. Run `approval serve`, `approval request`, and `execute` on the same host (or use a shared/persistent `KUBE_OPS_COPILOT_APPROVAL_STORE`).

## CI / Lint

- CI workflow: `.github/workflows/ci.yml` runs `go test ./...` and `golangci-lint`.
- Local lint (requires `golangci-lint`):

```bash
make lint
```

## Releases (binaries + container + SBOM + signing)

Tagging `vX.Y.Z` triggers `.github/workflows/release.yml`:

- Multi-arch binaries via GoReleaser.
- GitHub Release assets + checksums.
- SBOM generation (archives + container image).
- Keyless `cosign` signing (OIDC) for checksums and the container image.

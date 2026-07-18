# Production Deployment Runbook — AWS EC2 Singapore (ap-southeast-1)

**This document is a human-operated checklist. No step executes automatically.**
**All commands are reference text for a named human operator; this runbook does NOT execute them.**

---

## Scope

- Symbols: `DOGE-USDT`, `WIF-USDT` (spot grid only)
- Mean reversion: **excluded** — not modified, not enabled, not deployed
- Deployment location: AWS EC2 Singapore (`ap-southeast-1`)
- This spec supersedes all prior Tokyo deployment assumptions, IP allowlists, and network path references

## Prohibitions

- **No `sed`**: runtime behavior comes from version-controlled config validated at startup
- **No credential values** in this document, logs, configs, tests, or commit history
- **No Tokyo assumptions**: old region IPs, latency profiles, and allowlists are invalidated
- **Automated test passage does NOT constitute production approval**
- **No mean reversion activation** in any production profile

---

## Pre-Deployment Gate: Automated Acceptance

| Gate | Evidence Required | Status |
|------|-------------------|--------|
| All Property 1 (Bug Condition) tests pass on fixed code | `go test` output, zero failures | PENDING HUMAN VERIFICATION |
| All Property 2 (Preservation) tests pass | `go test` output, zero failures | PENDING HUMAN VERIFICATION |
| Integration/race/full suites green | `go test -race ./... -count=1` output | PENDING HUMAN VERIFICATION |
| Zero production DNS/dial/request in transport recorder | Test assertion output | PENDING HUMAN VERIFICATION |
| No credential values in test fixtures or logs | Static scan output | PENDING HUMAN VERIFICATION |

**Automated tests passing is a necessary but NOT sufficient condition for production deployment.**

---

## Step 1: Immutable Artifact and Config Verification

### 1.1 Binary Artifact Checksum

```text
# HUMAN: Build the artifact from the approved commit and record its SHA-256
sha256sum /opt/okx-hft-grid/okx-hft-grid
# Record: <ARTIFACT_SHA256> from commit <COMMIT_HASH>
```

- [ ] Artifact built from approved, tagged commit
- [ ] SHA-256 recorded and matches approved build log
- [ ] Binary has not been modified post-build

### 1.2 Config Checksum and Validation

```text
# HUMAN: Verify the deployed config matches the version-controlled profile
sha256sum /etc/okx-hft-grid/config.yaml
diff /etc/okx-hft-grid/config.yaml <source>/deploy/config.example.yaml
```

- [ ] Config file SHA-256 matches version-controlled artifact
- [ ] `deployment.location` = `aws-ap-southeast-1-singapore`
- [ ] `execution.td_mode` = `cash`
- [ ] `private_ws.heartbeat_interval` = `20s`
- [ ] `private_ws.liveness_timeout` = `45s`
- [ ] `private_ws.reconnect_start_deadline` = `5s`
- [ ] `reconciliation.interval` = `30s`
- [ ] `rebalancer.interval` = `30s`
- [ ] `ticker.max_age` = `5s`
- [ ] `adaptive_range`: symmetric, per-side half-width, min=0.015, max=0.04
- [ ] `persistence_path` = `/var/lib/okx-hft-grid/hft_state.db`
- [ ] `symbols` = exactly `["DOGE-USDT", "WIF-USDT"]`
- [ ] `mean_reversion_configs` = `[]` (empty)
- [ ] `websocket_url` = `wss://ws.okx.com:8443/ws/v5/public` (public market data role)
- [ ] `private_websocket_url` = `wss://ws.okx.com:8443/ws/v5/private` (private orders/fills role)
- [ ] `rest_url` = `https://www.okx.com`
- [ ] Public and private WebSocket endpoints are distinct (never the same URL)
- [ ] `trading_enabled` = `false` (reconcile-only staged deployment)
- [ ] No `sed` mutation required or planned
- [ ] No Tokyo-specific values present

---

## Step 2: Credential Security Gate

**All evidence below is PENDING HUMAN ACTION. This runbook does not execute credential operations.**

### 2.1 Revoke Old Credentials

- [ ] All previously exposed OKX API credentials have been revoked
- [ ] Evidence ID of revocation recorded: `________________`
- [ ] Old credentials confirmed non-functional (test call returns auth failure)
- [ ] Old Tokyo IP allowlists removed from OKX account

### 2.2 Create Minimum-Privilege Credentials

- [ ] New credentials created with **spot trade only** permissions
- [ ] Withdrawal: **DISABLED**
- [ ] Fund transfer: **DISABLED**
- [ ] Futures/margin/options: **DISABLED**
- [ ] IP allowlist restricted to approved Singapore EC2 instance IP(s) only
- [ ] Evidence ID of new credential creation: `________________`
- [ ] Evidence ID of permission verification: `________________`
- [ ] Evidence ID of Singapore IP allowlist: `________________`

### 2.3 Credential Delivery

- [ ] New credentials injected via environment file (`/etc/okx-hft-grid/env`)
- [ ] Environment file permissions: 0600, owned by `okxtrader:okxtrader`
- [ ] No credential value appears in config.yaml, logs, runbook evidence, or version control

---

## Step 3: Deployment Installation

**HUMAN OPERATOR performs these steps only after Steps 1 and 2 are complete.**

### 3.1 StateDirectory and WAL Backup

```text
# HUMAN: Back up existing state before any changes
sudo systemctl stop okx-hft-grid

# Back up current state directory including WAL files
sudo cp -a /var/lib/okx-hft-grid /var/lib/okx-hft-grid.backup.$(date +%Y%m%d_%H%M%S)

# If legacy path exists, back that up too
sudo cp -a /opt/okx-hft-grid/data /opt/okx-hft-grid/data.backup.$(date +%Y%m%d_%H%M%S) 2>/dev/null || true
```

- [ ] Service stopped cleanly (not killed)
- [ ] StateDirectory `/var/lib/okx-hft-grid` backed up with all WAL/SHM files
- [ ] Legacy `data/hft_state.db` (if exists) backed up with WAL/SHM files
- [ ] Backup integrity verified (file sizes non-zero, sqlite3 integrity_check)

### 3.2 Install Artifacts

```text
# HUMAN: Install binary and systemd units
sudo cp <built-binary> /opt/okx-hft-grid/okx-hft-grid
sudo cp deploy/okx-hft-grid.service /etc/systemd/system/
sudo cp deploy/okx-hft-grid-failure@.service /etc/systemd/system/
sudo systemctl daemon-reload
```

- [ ] Binary installed and SHA-256 re-verified on target
- [ ] systemd units installed: `StateDirectory=okx-hft-grid`, `ReadWritePaths=/var/lib/okx-hft-grid`
- [ ] `okx-hft-grid-failure@.service` has NO credential environment (confirmed)
- [ ] `daemon-reload` completed

### 3.3 Manual Deployment Approval

- [ ] Named human operator: `________________`
- [ ] Approval timestamp: `________________`
- [ ] Approval scope: reconcile-only start (trading_enabled=false)

---

## Step 4: Reconcile-Only Start (`trading_enabled=false`)

```text
# HUMAN: Start service in reconcile-only mode
sudo systemctl start okx-hft-grid
```

- [ ] Service starts and reaches `active (running)`
- [ ] `TimeoutStartSec=60s` not exceeded
- [ ] systemd notified READY (Type=notify)

### 4.1 Health and Journald Evidence

```text
# HUMAN: Verify health endpoint and journal output
curl -s http://localhost:9090/healthz
journalctl -u okx-hft-grid --since "5 minutes ago" --no-pager
```

- [ ] Health state: `degraded/reconciling` or `healthy` (NOT `safe-stopped` without reason)
- [ ] `location` = `aws-ap-southeast-1-singapore` in health output
- [ ] `state_directory_writable` = `true`
- [ ] Private_WS: authenticated, subscriptions confirmed
- [ ] Reconciliation: completed at least one cycle
- [ ] No credential values in journal output
- [ ] No Tokyo references in journal output
- [ ] `trading_enabled` = `false` confirmed in effective config log

### 4.2 State Migration Verification (if applicable)

- [ ] Migration marker present in `/var/lib/okx-hft-grid/`
- [ ] Legacy state preserved (not deleted)
- [ ] Fill watermarks, orders, positions intact
- [ ] No empty-state creation (row counts match or exceed backup)

### 4.3 Outbox and Recovery Verification

- [ ] Uncertain outbox effects recovered by deterministic clOrdId query
- [ ] No orphaned intents without terminal state
- [ ] Bot_Owned order lineage matches exchange query results

---

## Step 5: Independent Human Trading Approval

**This is a SEPARATE approval from Step 3.3. Automated test passage does NOT satisfy this gate.**

### 5.1 Pre-Approval Evidence Review

- [ ] Reconcile-only mode ran successfully for observation window: `___` minutes
- [ ] All reconciliation cycles completed within 30s schedule
- [ ] Private_WS stable (no unexpected disconnects)
- [ ] Fill watermarks consistent with exchange
- [ ] No Safe_Stop reasons active
- [ ] No unresolved Counter_SELL intents
- [ ] Health endpoint shows `healthy`

### 5.2 Deploy Trading-Enabled Config

```text
# HUMAN: Deploy a separately reviewed config revision with trading gates
# The config must contain all four gate evidence identifiers:
#   production_gates:
#     credential_rotation: "<evidence-id>"
#     least_privilege: "<evidence-id>"
#     singapore_ip_allowlist: "<evidence-id>"
#     human_trading_approval: "<evidence-id>"
# and trading_enabled: true
```

- [ ] Config revision reviewed by named human: `________________`
- [ ] All four production gate evidence fields populated (IDs only, no secrets)
- [ ] `trading_enabled: true` is the ONLY material change from reconcile-only config
- [ ] Mean reversion remains `[]` (empty)
- [ ] Config deployed and service restarted with new config
- [ ] Startup validation passes with all gates satisfied

### 5.3 Trading Approval Record

- [ ] Named human approver: `________________`
- [ ] Approval timestamp: `________________`
- [ ] Approval scope: production trading for DOGE-USDT and WIF-USDT

---

## Step 6: Post-Enable Observation Window

**Minimum observation: 30 minutes after trading enabled.**

- [ ] Both symbols placing and filling grid orders
- [ ] First reconciliation cycle post-enable completed
- [ ] BUY fill → Counter_SELL correlation observable in logs/metrics
- [ ] Counter_SELL latency within 5s initiation / 15s terminal SLO
- [ ] Rebalancer executing 30s cycles with fresh ticker
- [ ] No Safe_Stop activations without expected cause
- [ ] No credential leakage in any output
- [ ] Symbol isolation: DOGE failure (if any) does not stop WIF, and vice versa

---

## Step 7: Safe_Stop and Rollback Procedures

### 7.1 Emergency Safe_Stop

```text
# HUMAN: If unsafe condition detected
# The system should auto-Safe_Stop, but manual intervention may be needed:
sudo systemctl stop okx-hft-grid
```

- [ ] Confirm no new risk-increasing orders placed after stop
- [ ] Review journald for Safe_Stop reason codes
- [ ] Review exchange for any pending Bot_Owned orders

### 7.2 Rollback Procedure

**Rollback is permitted under these conditions:**

| Condition | Rollback to |
|-----------|-------------|
| Before new schema/outbox produces exchange effects | Previous binary + pre-migration backup DB |
| After new intents/effects exist | Only schema/outbox-compatible artifact; old unfixed binary MUST NOT resume trading |

```text
# HUMAN: Rollback steps
sudo systemctl stop okx-hft-grid

# Restore backup (ONLY if no new exchange effects produced)
sudo cp -a /var/lib/okx-hft-grid.backup.<timestamp>/* /var/lib/okx-hft-grid/

# Install previous compatible binary
sudo cp <previous-binary> /opt/okx-hft-grid/okx-hft-grid

# ALWAYS start with trading_enabled=false
# ALWAYS reconcile against exchange before any trading approval
sudo systemctl start okx-hft-grid
```

- [ ] Global Safe_Stop activated before artifact replacement
- [ ] Current DB + WAL + journal preserved (never overwrite newer state without reconciliation)
- [ ] Rollback config has `trading_enabled=false`
- [ ] Deterministic effect recovery and exchange reconciliation completed
- [ ] Credential rotation is NOT rolled back (old credentials remain revoked)
- [ ] Human approval required again before any trading resumes

---

## Appendix: Verification Checklist Summary

| Item | Requirement |
|------|-------------|
| Public WebSocket endpoint | `websocket_url` = `wss://ws.okx.com:8443/ws/v5/public` |
| Private WebSocket endpoint | `private_websocket_url` = `wss://ws.okx.com:8443/ws/v5/private` |
| Endpoint role separation | Public and private WebSocket URLs must be distinct |
| No `sed` dependency | Runtime behavior from version-controlled config only |
| Singapore location | All artifacts, health, config reference `ap-southeast-1` |
| No Tokyo assumptions | Old IPs, latency, paths invalidated |
| Credential rotation | Old revoked, new minimum-privilege, Singapore IP only |
| `trading_enabled=false` first | Reconcile-only validates before any trading |
| StateDirectory + WAL backup | `/var/lib/okx-hft-grid` backed up with WAL |
| Health/journald evidence | Machine-readable health, structured journal logs |
| Independent trading approval | Named human, separate from deployment approval |
| Observation window | Minimum 30 minutes post-enable |
| Safe_Stop + rollback | Schema-compatible only, reconcile-first, no old credentials |
| Mean reversion excluded | Not in production profile, not modified, not enabled |
| No credential values | Never in config, logs, runbook, tests, or VCS |

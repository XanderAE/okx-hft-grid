# OKX HFT Grid Trading System - Deployment Guide

## Prerequisites

- Linux server (Ubuntu 22.04+ recommended)
- systemd
- Go 1.21+ (for building)

## Build

```bash
cd /path/to/source
go build -o okx-hft-grid ./cmd/
```

## Installation

### 1. Create dedicated user

```bash
sudo useradd -r -s /usr/sbin/nologin okxtrader
```

### 2. Install binary

```bash
sudo mkdir -p /opt/okx-hft-grid
sudo cp okx-hft-grid /opt/okx-hft-grid/
sudo chown okxtrader:okxtrader /opt/okx-hft-grid/okx-hft-grid
sudo chmod 750 /opt/okx-hft-grid/okx-hft-grid
```

### 3. Install configuration

```bash
sudo mkdir -p /etc/okx-hft-grid
sudo cp config.yaml /etc/okx-hft-grid/
sudo cp env.example /etc/okx-hft-grid/env
sudo chown -R okxtrader:okxtrader /etc/okx-hft-grid
sudo chmod 600 /etc/okx-hft-grid/env
sudo chmod 600 /etc/okx-hft-grid/config.yaml
```

Edit `/etc/okx-hft-grid/env` with your actual API credentials.

### 4. Install systemd service

```bash
sudo cp deploy/okx-hft-grid.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable okx-hft-grid
```

## Service Management

```bash
# Start the service
sudo systemctl start okx-hft-grid

# Stop the service
sudo systemctl stop okx-hft-grid

# Check status
sudo systemctl status okx-hft-grid

# View logs
sudo journalctl -u okx-hft-grid -f

# View recent logs
sudo journalctl -u okx-hft-grid --since "1 hour ago"
```

## Restart Behavior

The service is configured to:
- Restart on failure with a 5-second delay
- Allow at most 3 restarts within a 60-second window
- After exceeding the restart limit, manual intervention is required

To reset the restart counter:

```bash
sudo systemctl reset-failed okx-hft-grid
sudo systemctl start okx-hft-grid
```

## Security

- Runs as non-root user (`okxtrader`)
- `ProtectSystem=strict`: filesystem is read-only except for explicitly allowed paths
- `ProtectHome=yes`: `/home`, `/root`, `/run/user` are inaccessible
- `NoNewPrivileges=yes`: prevents privilege escalation
- Environment file permissions set to 600 (owner-only read/write)
- API credentials stored in environment file, never in config.yaml

## Log Management

Logs are sent to journald with identifier `okx-hft-grid`. Configure journald retention in `/etc/systemd/journald.conf` to keep at least 30 days:

```ini
[Journal]
MaxRetentionSec=30day
```

Reload journald after changes:

```bash
sudo systemctl restart systemd-journald
```

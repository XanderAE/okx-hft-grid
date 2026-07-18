# Bugfix Requirements Document

## Introduction

This document defines the requirements for correcting the Singapore EC2 systemd startup failure in which `okx-hft-grid.service` cannot read `/etc/okx-hft-grid/config.yaml` after switching to the dedicated `okxtrader` service identity. The earlier `production-grid-stabilization` spec remains the baseline for runtime state, hardening, reconcile-only startup, and trading safety. This focused follow-up spec is separate because the concrete read-permission failure and the fresh-install ownership contract for `/etc/okx-hft-grid` were not explicitly covered by that completed stabilization work.

The fix must make service startup repeatable with least-privilege access, preserve all existing systemd hardening, keep `trading_enabled=false`, and produce verification evidence without placing orders or exposing credentials.

```text
FUNCTION isBugCondition(X)
  INPUT: X of type SystemdServiceStart
  OUTPUT: boolean

  RETURN X.serviceIsExpectedToStart
     AND (NOT X.serviceIdentityIsResolvable
          OR NOT X.configDirectoryIsTraversableByService
          OR NOT X.configFileIsReadableByService
          OR NOT X.requiredEnvironmentIsLoadableForService)
END FUNCTION

// Property: Fix Checking
FOR ALL X WHERE isBugCondition(X) DO
  result ← F'(X)
  ASSERT result.serviceIsActive
     AND result.configurationLoaded
     AND result.requiredEnvironmentAvailable
     AND result.accessIsLeastPrivilege
     AND result.systemdHardeningIsPreserved
     AND result.tradingEnabled = false
     AND result.ordersPlaced = 0
     AND result.verificationContainsNoSecretValues
END FOR

// Property: Preservation Checking
FOR ALL X WHERE NOT isBugCondition(X) DO
  ASSERT observableTradingSafetyAndHardening(F(X))
       = observableTradingSafetyAndHardening(F'(X))
END FOR
```

## Bug Analysis

### Current Behavior (Defect)

The deployed unit is configured to run as `User=okxtrader` and `Group=okxtrader`, but the installation procedure did not establish and verify a complete least-privilege read path for the root-controlled configuration inputs before starting the unit.

1.1 WHEN `okx-hft-grid.service` is started before the `okxtrader` system account and group exist or can be resolved THEN systemd terminates startup with `status=217/USER` before the application runs.

1.2 WHEN the `okxtrader` identity exists but any directory component leading to `/etc/okx-hft-grid/config.yaml` is not traversable by that identity, or the file is not readable by that identity, THEN the application exits with `Failed to load configuration` and `permission denied`, preventing the service from reaching reconcile-only operation.

1.3 WHEN `/etc/okx-hft-grid/env` is installed with production credentials THEN the current deployment does not verify before startup that the required environment can be loaded by the service while remaining unavailable for modification by the service and unavailable to unrelated users.

1.4 WHEN an operator follows the current Singapore production runbook on a fresh host THEN the documented artifact-installation flow does not fully create and verify the service identity, configuration directory, configuration file, environment file, and their effective access as one prerequisite to starting the unit, allowing the same startup failure to recur.

1.5 WHEN startup fails with `217/USER` or configuration `permission denied` THEN the service exits before it can prove successful reconcile-only initialization, and the available status and journal evidence cannot demonstrate a running non-trading service.

### Expected Behavior (Correct)

The corrected deployment must establish the minimum access required for startup while keeping configuration and credentials under administrative control.

2.1 WHEN a fresh or repaired installation prepares `okx-hft-grid.service` THEN the system SHALL create or verify the dedicated `okxtrader` system account and group before enabling or starting the unit, and the unit's configured user and group SHALL resolve successfully.

2.2 WHEN `okx-hft-grid.service` starts as `okxtrader:okxtrader` and reads `/etc/okx-hft-grid/config.yaml` THEN the system SHALL provide traversal of the required parent directories and read access to the configuration file for that service identity, while keeping the directory and file administratively owned, not writable by the service, and inaccessible to unrelated users.

2.3 WHEN systemd loads `/etc/okx-hft-grid/env` for the service THEN the system SHALL make the required environment values available to the process through only the principals needed for service startup, keep the file administratively owned and not writable by the service, deny access to unrelated users, and avoid emitting credential values through status, journal, command output, or documentation.

2.4 WHEN an operator performs a fresh Singapore installation or repairs this failure THEN the system SHALL provide a version-controlled, repeatable deployment and runbook procedure that establishes and verifies the account and least-privilege access for the configuration directory, configuration file, and environment file before service start; the procedure SHALL NOT depend on `sed`, world-readable or world-writable permissions, or ad hoc post-start mutation.

2.5 WHEN the corrected service is started with the approved configuration containing `trading_enabled=false` THEN the system SHALL allow verification through systemd service status and the service journal that the unit is active, configuration and required environment loading succeeded, reconcile-only initialization was reached, neither `217/USER` nor `permission denied` occurred, no credential value was disclosed, and no order was placed.

### Unchanged Behavior (Regression Prevention)

The permission correction is limited to service identity setup, read access to startup inputs, and deployment guidance. It must not weaken the production safety baseline or enable trading.

3.1 WHEN the service starts after the permission correction THEN the system SHALL CONTINUE TO run the application as the non-root `okxtrader` user and `okxtrader` group rather than granting root execution.

3.2 WHEN systemd applies the production unit sandbox THEN the system SHALL CONTINUE TO enforce `ProtectSystem=strict`, `ProtectHome=yes`, `NoNewPrivileges=yes`, the restrictive umask, and all other existing hardening; the fix SHALL NOT replace sandboxing with broad filesystem access.

3.3 WHEN the application needs mutable runtime state THEN the system SHALL CONTINUE TO use the systemd-managed `/var/lib/okx-hft-grid` StateDirectory with its existing restricted mode and explicit write allowance, and SHALL NOT make `/etc/okx-hft-grid` or `/opt/okx-hft-grid` writable to solve this bug.

3.4 WHEN the approved staged production configuration has `trading_enabled=false` THEN the system SHALL CONTINUE TO operate only in reconcile-only mode and SHALL NOT place, amend, or cancel orders as part of startup or permission verification.

3.5 WHEN valid production configuration is loaded THEN the system SHALL CONTINUE TO preserve the approved Singapore location, spot `cash` mode, DOGE-USDT and WIF-USDT scope, timing and reconciliation settings, persistence path, and disabled mean-reversion configuration.

3.6 WHEN credentials are supplied through the required environment THEN the system SHALL CONTINUE TO keep credential values out of `config.yaml`, version control, runbook evidence, systemd status, journald output, health output, and other operator-visible diagnostics.

3.7 WHEN the process exits unexpectedly for reasons unrelated to this permission defect THEN the system SHALL CONTINUE TO use the existing notify readiness, restart delay, restart limit, failure handling, and journald behavior defined by the production unit.

3.8 WHEN deployment artifacts or production settings are changed in the future THEN the system SHALL CONTINUE TO require version-controlled files and explicit human review rather than `sed`-based production mutations.

package persistence

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// --- StateDirectory validation and writable probe tests ---

func TestStateDirectoryUnit_RejectsEmptyPath(t *testing.T) {
	err := ValidateStateDirectory("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStateDirectoryUnit_RejectsRelativePath(t *testing.T) {
	err := ValidateStateDirectory("data/hft_state")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStateDirectoryUnit_RejectsNonExistentDir(t *testing.T) {
	err := ValidateStateDirectory(filepath.Join(t.TempDir(), "nonexistent", "dir"))
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestStateDirectoryUnit_RejectsFilePath(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	err := ValidateStateDirectory(filePath)
	if err == nil {
		t.Fatal("expected error for file path (not directory)")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStateDirectoryUnit_AcceptsValidAbsoluteDir(t *testing.T) {
	dir := t.TempDir()
	err := ValidateStateDirectory(dir)
	if err != nil {
		t.Fatalf("unexpected error for valid directory: %v", err)
	}
}

func TestStateDirectoryUnit_RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test not reliable on Windows without admin")
	}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	linkDir := filepath.Join(dir, "link")
	if err := os.Mkdir(realDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	err := ValidateStateDirectory(linkDir)
	if err == nil {
		t.Fatal("expected error for symlinked path")
	}
	if !strings.Contains(err.Error(), "non-canonical") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWritableProbe_SucceedsOnWritableDir(t *testing.T) {
	dir := t.TempDir()
	err := WritableProbe(dir)
	if err != nil {
		t.Fatalf("WritableProbe() failed on writable dir: %v", err)
	}
	// Verify no probe file was left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".writable_probe_") {
			t.Fatalf("probe file not cleaned up: %s", e.Name())
		}
	}
}

func TestWritableProbe_FailsOnReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory semantics differ on Windows")
	}
	dir := t.TempDir()
	// Make directory read-only
	if err := os.Chmod(dir, 0555); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	defer os.Chmod(dir, 0755) // restore for cleanup

	err := WritableProbe(dir)
	if err == nil {
		t.Fatal("expected error for read-only directory")
	}
}

func TestWritableProbe_FailsOnInvalidDir(t *testing.T) {
	err := WritableProbe("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

// --- Legacy migration tests ---

func createLegacyDB(t *testing.T, path string, withData bool) {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("create legacy db: %v", err)
	}
	defer db.Close()

	// Force WAL mode
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("set WAL mode: %v", err)
	}

	// Create legacy tables
	stmts := []string{
		`CREATE TABLE orders (
			order_id TEXT PRIMARY KEY,
			exchange_order_id TEXT NOT NULL DEFAULT '',
			symbol TEXT NOT NULL,
			side INTEGER NOT NULL,
			price TEXT NOT NULL,
			quantity TEXT NOT NULL,
			filled_qty TEXT NOT NULL DEFAULT '0',
			status INTEGER NOT NULL,
			strategy_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE positions (
			symbol TEXT PRIMARY KEY,
			side INTEGER NOT NULL,
			quantity TEXT NOT NULL,
			avg_entry_price TEXT NOT NULL,
			unrealized_pnl TEXT NOT NULL DEFAULT '0',
			realized_pnl TEXT NOT NULL DEFAULT '0'
		)`,
		`CREATE TABLE strategy_state (
			strategy_id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 0,
			config_json BLOB,
			state_json BLOB
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	if withData {
		_, err = db.Exec(`INSERT INTO orders (order_id, symbol, side, price, quantity, status, strategy_id, created_at, updated_at)
			VALUES ('ord-1', 'DOGE-USDT', 0, '0.085', '1000', 1, 'grid-doge', 1700000000, 1700000001)`)
		if err != nil {
			t.Fatalf("insert order: %v", err)
		}
		_, err = db.Exec(`INSERT INTO positions (symbol, side, quantity, avg_entry_price, unrealized_pnl, realized_pnl)
			VALUES ('DOGE-USDT', 0, '500', '0.084', '1.5', '3.2')`)
		if err != nil {
			t.Fatalf("insert position: %v", err)
		}
		_, err = db.Exec(`INSERT INTO strategy_state (strategy_id, type, is_active, config_json, state_json)
			VALUES ('grid-doge', 'grid', 1, '{"levels":20}', '{"active":true}')`)
		if err != nil {
			t.Fatalf("insert strategy_state: %v", err)
		}
	}
}

func createLegacyDBWithWALRows(t *testing.T, path string) {
	t.Helper()
	// Create the DB with initial data
	createLegacyDB(t, path, true)

	// Open again and insert more rows WITHOUT checkpointing
	// This ensures the WAL file contains committed but un-checkpointed data
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=FULL")
	if err != nil {
		t.Fatalf("reopen legacy db: %v", err)
	}
	defer db.Close()

	// Force WAL mode
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("set WAL: %v", err)
	}

	// Insert additional rows that will live only in WAL
	for i := 0; i < 5; i++ {
		_, err = db.Exec(`INSERT INTO orders (order_id, symbol, side, price, quantity, status, strategy_id, created_at, updated_at)
			VALUES (?, 'WIF-USDT', 0, '2.50', '100', 1, 'grid-wif', 1700000100, 1700000101)`,
			fmt.Sprintf("wal-ord-%d", i))
		if err != nil {
			t.Fatalf("insert WAL order: %v", err)
		}
	}

	// Explicitly do NOT checkpoint - these rows should be in the WAL file
	// The -wal file should exist after this
}

func TestLegacyMigrationWithWAL_PreservesWALRows(t *testing.T) {
	stateDir := t.TempDir()
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "hft_state.db")

	createLegacyDBWithWALRows(t, legacyPath)

	// Verify WAL file exists (data is in WAL)
	walPath := legacyPath + "-wal"
	if _, err := os.Stat(walPath); err != nil {
		// WAL may have been auto-checkpointed. The test still validates
		// that VACUUM INTO captures all committed data regardless.
		t.Logf("WAL file not found (auto-checkpointed), migration still validates data: %v", err)
	}

	result, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      legacyPath,
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err != nil {
		t.Fatalf("MigrateLegacyState() error: %v", err)
	}

	if result.Action != "migrated" {
		t.Fatalf("expected action=migrated, got %s", result.Action)
	}

	// Verify all rows including WAL-only rows are present in the target
	targetDB, err := sql.Open("sqlite3", result.TargetPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer targetDB.Close()

	var orderCount int64
	err = targetDB.QueryRow("SELECT COUNT(*) FROM orders").Scan(&orderCount)
	if err != nil {
		t.Fatalf("count orders: %v", err)
	}
	// 1 initial + 5 WAL-only = 6 total
	if orderCount != 6 {
		t.Fatalf("expected 6 orders (including WAL rows), got %d", orderCount)
	}

	// Verify WAL-specific rows
	var walOrderCount int64
	err = targetDB.QueryRow("SELECT COUNT(*) FROM orders WHERE order_id LIKE 'wal-ord-%'").Scan(&walOrderCount)
	if err != nil {
		t.Fatalf("count WAL orders: %v", err)
	}
	if walOrderCount != 5 {
		t.Fatalf("expected 5 WAL-only orders, got %d", walOrderCount)
	}

	// Verify fingerprint was recorded
	var fp string
	err = targetDB.QueryRow("SELECT value FROM migration_metadata WHERE key='source_fingerprint'").Scan(&fp)
	if err != nil {
		t.Fatalf("read fingerprint: %v", err)
	}
	if fp == "" {
		t.Fatal("fingerprint is empty")
	}

	// Verify legacy is preserved (not modified or deleted)
	if !fileExists(legacyPath) {
		t.Fatal("legacy database was removed during migration")
	}
}

func TestMigrationConflictFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	legacyDir := t.TempDir()

	legacyPath := filepath.Join(legacyDir, "hft_state.db")
	targetPath := filepath.Join(stateDir, "hft_state.db")

	// Create legacy DB with data
	createLegacyDB(t, legacyPath, true)

	// Create a DIFFERENT target DB (conflict - no migration marker)
	createLegacyDB(t, targetPath, false)

	_, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      legacyPath,
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err == nil {
		t.Fatal("expected error for conflicting target/legacy without marker")
	}
	if !strings.Contains(err.Error(), "conflict") && !strings.Contains(err.Error(), "migration marker") {
		t.Fatalf("expected conflict error, got: %v", err)
	}

	// Both files must be preserved
	if !fileExists(legacyPath) {
		t.Fatal("legacy was destroyed during conflict")
	}
	if !fileExists(targetPath) {
		t.Fatal("target was destroyed during conflict")
	}
}

func TestMigrationConflictFailsClosed_PermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on Windows")
	}
	stateDir := t.TempDir()
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "hft_state.db")

	createLegacyDB(t, legacyPath, true)

	// Make state directory read-only so rename will fail
	os.Chmod(stateDir, 0555)
	defer os.Chmod(stateDir, 0755)

	_, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      legacyPath,
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err == nil {
		t.Fatal("expected error for permission denied migration")
	}
}

func TestMigrationConflictFailsClosed_EmptyStateNotApproved(t *testing.T) {
	stateDir := t.TempDir()

	_, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      filepath.Join(stateDir, "nonexistent.db"),
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err == nil {
		t.Fatal("expected error when neither legacy nor target exists and empty not approved")
	}
	if !strings.Contains(err.Error(), "state_bootstrap_mode") || !strings.Contains(err.Error(), "allow-new") {
		t.Fatalf("expected empty-state error mentioning allow-new, got: %v", err)
	}
}

func TestMigrationConflictFailsClosed_EmptyStateApproved(t *testing.T) {
	stateDir := t.TempDir()

	result, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      filepath.Join(stateDir, "nonexistent.db"),
		TargetDBName:    "hft_state.db",
		AllowEmptyState: true,
	})
	if err != nil {
		t.Fatalf("MigrateLegacyState() error: %v", err)
	}
	if result.Action != "bootstrapped-empty" {
		t.Fatalf("expected bootstrapped-empty, got %s", result.Action)
	}
}

func TestAtomicPublish_NoPartialDB(t *testing.T) {
	stateDir := t.TempDir()
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "hft_state.db")

	createLegacyDB(t, legacyPath, true)

	result, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      legacyPath,
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err != nil {
		t.Fatalf("MigrateLegacyState() error: %v", err)
	}

	if result.Action != "migrated" {
		t.Fatalf("expected migrated, got %s", result.Action)
	}

	// Verify no temp files remain in state directory
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ".tmp.") {
			t.Fatalf("temp file not cleaned up: %s", name)
		}
		if name == "migration.lock" {
			t.Fatalf("migration lock not released")
		}
	}

	// Verify target is valid SQLite
	targetDB, err := sql.Open("sqlite3", result.TargetPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer targetDB.Close()

	var integrityResult string
	if err := targetDB.QueryRow("PRAGMA integrity_check").Scan(&integrityResult); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if integrityResult != "ok" {
		t.Fatalf("target integrity failed: %s", integrityResult)
	}

	// Verify data was actually migrated
	var orderID string
	err = targetDB.QueryRow("SELECT order_id FROM orders WHERE order_id='ord-1'").Scan(&orderID)
	if err != nil {
		t.Fatalf("query migrated order: %v", err)
	}
	if orderID != "ord-1" {
		t.Fatalf("expected ord-1, got %s", orderID)
	}
}

func TestAtomicPublish_IdempotentRerun(t *testing.T) {
	stateDir := t.TempDir()
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "hft_state.db")

	createLegacyDB(t, legacyPath, true)

	cfg := MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      legacyPath,
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	}

	// First migration
	result1, err := MigrateLegacyState(cfg)
	if err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if result1.Action != "migrated" {
		t.Fatalf("first: expected migrated, got %s", result1.Action)
	}

	// Second migration (idempotent) - should detect existing target with marker
	result2, err := MigrateLegacyState(cfg)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if result2.Action != "already-migrated" {
		t.Fatalf("second: expected already-migrated, got %s", result2.Action)
	}
}

func TestMigrationConflictFailsClosed_CorruptedLegacy(t *testing.T) {
	stateDir := t.TempDir()
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "hft_state.db")

	// Create a corrupted "database" file
	if err := os.WriteFile(legacyPath, []byte("this is not a sqlite database"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      legacyPath,
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err == nil {
		t.Fatal("expected error for corrupted legacy database")
	}
	// Legacy must be preserved
	if !fileExists(legacyPath) {
		t.Fatal("corrupted legacy was removed")
	}
}

func TestLegacyMigrationWithWAL_TargetExistsValid(t *testing.T) {
	stateDir := t.TempDir()
	targetPath := filepath.Join(stateDir, "hft_state.db")

	// Create a valid target directly (simulates previous successful migration)
	createLegacyDB(t, targetPath, true)

	result, err := MigrateLegacyState(MigrationConfig{
		StateDirectory:  stateDir,
		LegacyPath:      "",
		TargetDBName:    "hft_state.db",
		AllowEmptyState: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "opened-existing" {
		t.Fatalf("expected opened-existing, got %s", result.Action)
	}
}

// --- systemd unit file fixture tests ---

func TestRestartPolicyFixture_ServiceDirectives(t *testing.T) {
	// Read and parse the actual systemd unit file to verify required directives
	servicePath := filepath.Join("..", "..", "deploy", "okx-hft-grid.service")
	data, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read service file: %v", err)
	}
	content := string(data)

	required := map[string]string{
		"StateDirectory":        "okx-hft-grid",
		"StateDirectoryMode":    "0750",
		"ReadWritePaths":        "/var/lib/okx-hft-grid",
		"ProtectSystem":         "strict",
		"Type":                  "notify",
		"RestartSec":            "5s",
		"StartLimitBurst":       "3",
		"StartLimitIntervalSec": "60s",
	}

	for key, expected := range required {
		directive := key + "=" + expected
		if !strings.Contains(content, directive) {
			t.Errorf("missing required directive: %s", directive)
		}
	}

	// Verify OnFailure references the failure unit
	if !strings.Contains(content, "OnFailure=okx-hft-grid-failure@%n.service") {
		t.Error("missing OnFailure=okx-hft-grid-failure@%n.service")
	}

	// Verify no credential environment variables are present in the unit
	for _, secret := range []string{"OKX_API_KEY", "OKX_SECRET_KEY", "OKX_PASSPHRASE"} {
		if strings.Contains(content, secret) {
			t.Errorf("unit file must not contain credential: %s", secret)
		}
	}
}

func TestRestartPolicyFixture_FailureUnitHasNoCredentials(t *testing.T) {
	failurePath := filepath.Join("..", "..", "deploy", "okx-hft-grid-failure@.service")
	data, err := os.ReadFile(failurePath)
	if err != nil {
		t.Fatalf("read failure unit: %v", err)
	}
	content := string(data)

	// Must be oneshot
	if !strings.Contains(content, "Type=oneshot") {
		t.Error("failure unit must be Type=oneshot")
	}

	// Must use journald
	if !strings.Contains(content, "StandardOutput=journal") {
		t.Error("failure unit must output to journal")
	}

	// Must NOT have EnvironmentFile or credential references
	for _, forbidden := range []string{"EnvironmentFile", "OKX_API_KEY", "OKX_SECRET_KEY", "OKX_PASSPHRASE"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("failure unit must not contain: %s", forbidden)
		}
	}
}

func TestRestartPolicyFixture_RestartBehavior(t *testing.T) {
	servicePath := filepath.Join("..", "..", "deploy", "okx-hft-grid.service")
	data, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read service file: %v", err)
	}
	content := string(data)

	// Verify restart on failure
	if !strings.Contains(content, "Restart=on-failure") {
		t.Error("missing Restart=on-failure")
	}

	// Verify restart interval is 5 seconds
	if !strings.Contains(content, "RestartSec=5s") {
		t.Error("missing RestartSec=5s")
	}

	// Verify burst limits
	if !strings.Contains(content, "StartLimitBurst=3") {
		t.Error("missing StartLimitBurst=3")
	}
	if !strings.Contains(content, "StartLimitIntervalSec=60s") {
		t.Error("missing StartLimitIntervalSec=60s")
	}

	// Verify TimeoutStartSec for readiness budget
	if !strings.Contains(content, "TimeoutStartSec=60s") {
		t.Error("missing TimeoutStartSec=60s for readiness budget")
	}
}

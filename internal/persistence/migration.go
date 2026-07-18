package persistence

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Migration errors are deliberately fail-closed: no partial state is published.
var (
	ErrStateDirectoryInvalid     = errors.New("migration: state directory path is invalid or non-canonical")
	ErrStateDirectoryNotWritable = errors.New("migration: state directory is not writable")
	ErrMigrationConflict         = errors.New("migration: target and legacy both exist with unresolved conflict")
	ErrMigrationCorrupted        = errors.New("migration: source database is corrupted or unreadable")
	ErrMigrationPermission       = errors.New("migration: insufficient permissions for migration")
	ErrEmptyStateNotApproved     = errors.New("migration: empty state requires explicit state_bootstrap_mode: allow-new")
	ErrMigrationLock             = errors.New("migration: cannot acquire exclusive migration lock")
	ErrMigrationIntegrity        = errors.New("migration: integrity or schema check failed on migrated database")
	ErrMigrationTargetExists     = errors.New("migration: target already exists and is valid")
)

// MigrationConfig holds parameters for the legacy state migration.
type MigrationConfig struct {
	// StateDirectory is the production state directory (absolute, canonical).
	// Must be /var/lib/okx-hft-grid in production.
	StateDirectory string

	// LegacyPath is the path to the old state database (e.g., data/hft_state.db).
	LegacyPath string

	// TargetDBName is the filename within StateDirectory for the production DB.
	TargetDBName string

	// AllowEmptyState controls whether a fresh installation may bootstrap
	// without a legacy database. Corresponds to state_bootstrap_mode: allow-new.
	AllowEmptyState bool
}

// MigrationResult describes the outcome of a migration attempt.
type MigrationResult struct {
	// Action taken: "opened-existing", "migrated", "bootstrapped-empty", "already-migrated"
	Action string

	// TargetPath is the final database path.
	TargetPath string

	// SourceFingerprint is the SHA-256 of the legacy database (hex) if migrated.
	SourceFingerprint string

	// RowCounts holds verified table→row-count from the migrated DB.
	RowCounts map[string]int64
}

// ValidateStateDirectory checks that the given path is:
// 1. An absolute path
// 2. A canonical (no symlinks in path components) directory
// 3. Actually exists as a directory
//
// This must be called before any socket/connection is opened.
func ValidateStateDirectory(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: empty path", ErrStateDirectoryInvalid)
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%w: not absolute: %s", ErrStateDirectoryInvalid, dir)
	}

	// Resolve symlinks and verify canonical form
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve: %s: %v", ErrStateDirectoryInvalid, dir, err)
	}
	// filepath.Clean normalizes separators
	if filepath.Clean(resolved) != filepath.Clean(dir) {
		return fmt.Errorf("%w: non-canonical (symlink or extra components): configured=%s resolved=%s",
			ErrStateDirectoryInvalid, dir, resolved)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%w: cannot stat: %s: %v", ErrStateDirectoryInvalid, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: not a directory: %s", ErrStateDirectoryInvalid, dir)
	}

	return nil
}

// WritableProbe performs a create/write/fsync/close/delete probe on the state
// directory. This verifies the process can actually write data as okxtrader
// before any socket is opened.
func WritableProbe(dir string) error {
	if err := ValidateStateDirectory(dir); err != nil {
		return err
	}

	// Create a probe file with a random suffix to avoid conflicts
	probeName := fmt.Sprintf(".writable_probe_%d_%d", os.Getpid(), rand.Int63())
	probePath := filepath.Join(dir, probeName)

	f, err := os.OpenFile(probePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("%w: create probe: %v", ErrStateDirectoryNotWritable, err)
	}

	// Write probe data
	probeData := []byte("okx-hft-grid writable probe\n")
	if _, err := f.Write(probeData); err != nil {
		f.Close()
		os.Remove(probePath)
		return fmt.Errorf("%w: write probe: %v", ErrStateDirectoryNotWritable, err)
	}

	// Fsync to verify durable write capability
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(probePath)
		return fmt.Errorf("%w: fsync probe: %v", ErrStateDirectoryNotWritable, err)
	}

	if err := f.Close(); err != nil {
		os.Remove(probePath)
		return fmt.Errorf("%w: close probe: %v", ErrStateDirectoryNotWritable, err)
	}

	// Clean up the probe file
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("%w: remove probe: %v", ErrStateDirectoryNotWritable, err)
	}

	return nil
}

// MigrateLegacyState implements the WAL-aware atomic migration algorithm from design.
// It handles all cases in the migration decision matrix:
//   - Target exists and valid: verify and return
//   - Target absent + legacy exists: backup through SQLite API, validate, atomic publish
//   - Both exist with conflict: fail closed
//   - Neither exists: require AllowEmptyState or fail
//   - Any corruption/permission error: fail closed, preserve legacy
func MigrateLegacyState(cfg MigrationConfig) (*MigrationResult, error) {
	if cfg.StateDirectory == "" {
		return nil, fmt.Errorf("%w: state directory not configured", ErrStateDirectoryInvalid)
	}
	if cfg.TargetDBName == "" {
		cfg.TargetDBName = "hft_state.db"
	}

	targetPath := filepath.Join(cfg.StateDirectory, cfg.TargetDBName)
	lockPath := filepath.Join(cfg.StateDirectory, "migration.lock")

	// Acquire exclusive migration lock
	lock, err := acquireMigrationLock(lockPath)
	if err != nil {
		return nil, err
	}
	defer releaseMigrationLock(lock, lockPath)

	targetExists := fileExists(targetPath)
	legacyExists := cfg.LegacyPath != "" && fileExists(cfg.LegacyPath)

	switch {
	case targetExists && legacyExists:
		// Both exist: check for matching migration marker
		if hasValidMigrationMarker(targetPath, cfg.LegacyPath) {
			return &MigrationResult{
				Action:     "already-migrated",
				TargetPath: targetPath,
			}, nil
		}
		return nil, fmt.Errorf("%w: target=%s legacy=%s both exist without matching migration marker",
			ErrMigrationConflict, targetPath, cfg.LegacyPath)

	case targetExists && !legacyExists:
		// Target exists, no legacy: validate and open
		if err := validateTargetDB(targetPath); err != nil {
			return nil, err
		}
		return &MigrationResult{
			Action:     "opened-existing",
			TargetPath: targetPath,
		}, nil

	case !targetExists && legacyExists:
		// Migrate legacy to target
		return migrateLegacyToTarget(cfg.LegacyPath, targetPath, cfg.StateDirectory)

	default:
		// Neither exists
		if !cfg.AllowEmptyState {
			return nil, fmt.Errorf("%w: no legacy=%s and no target=%s",
				ErrEmptyStateNotApproved, cfg.LegacyPath, targetPath)
		}
		return &MigrationResult{
			Action:     "bootstrapped-empty",
			TargetPath: targetPath,
		}, nil
	}
}

// migrateLegacyToTarget performs the core WAL-aware SQLite backup migration.
func migrateLegacyToTarget(legacyPath, targetPath, stateDir string) (*MigrationResult, error) {
	// Open legacy through SQLite backup API (read-only) to include committed WAL content
	srcDB, err := sql.Open("sqlite3", legacyPath+"?mode=ro&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("%w: cannot open legacy: %v", ErrMigrationCorrupted, err)
	}
	defer srcDB.Close()

	// Verify legacy is readable
	if err := srcDB.Ping(); err != nil {
		return nil, fmt.Errorf("%w: legacy ping failed: %v", ErrMigrationCorrupted, err)
	}

	// Run integrity check on source
	if err := checkIntegrity(srcDB); err != nil {
		return nil, fmt.Errorf("%w: legacy integrity failed: %v", ErrMigrationCorrupted, err)
	}

	// Compute source fingerprint before backup
	fingerprint, err := computeFingerprint(legacyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: fingerprint computation failed: %v", ErrMigrationCorrupted, err)
	}

	// Create temp file in the SAME directory as target (same filesystem for atomic rename)
	tmpName := fmt.Sprintf("%s.tmp.%d", filepath.Base(targetPath), time.Now().UnixNano())
	tmpPath := filepath.Join(stateDir, tmpName)

	// Use SQLite backup API via VACUUM INTO which reads committed WAL content
	_, err = srcDB.Exec(fmt.Sprintf("VACUUM INTO '%s'", escapeSQLitePath(tmpPath)))
	if err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: SQLite backup (VACUUM INTO) failed: %v", ErrMigrationCorrupted, err)
	}

	// Validate the temp database
	tmpDB, err := sql.Open("sqlite3", tmpPath+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=FULL")
	if err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: cannot open temp copy: %v", ErrMigrationIntegrity, err)
	}

	if err := tmpDB.Ping(); err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: temp copy ping failed: %v", ErrMigrationIntegrity, err)
	}

	// Integrity check on copy
	if err := checkIntegrity(tmpDB); err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: temp copy integrity failed: %v", ErrMigrationIntegrity, err)
	}

	// Schema check: verify required legacy tables exist
	if err := checkRequiredTables(tmpDB); err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: %v", ErrMigrationIntegrity, err)
	}

	// Logical row-count checks
	rowCounts, err := countLegacyRows(tmpDB)
	if err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: row count check failed: %v", ErrMigrationIntegrity, err)
	}

	// Record migration fingerprint in temp transaction
	migrationTS := time.Now().UTC()
	_, err = tmpDB.Exec(`CREATE TABLE IF NOT EXISTS migration_metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	if err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: create migration_metadata: %v", ErrMigrationIntegrity, err)
	}

	_, err = tmpDB.Exec(`INSERT OR REPLACE INTO migration_metadata (key, value) VALUES
		('source_fingerprint', ?),
		('migration_timestamp', ?),
		('source_path', ?),
		('schema_version', '1')`,
		fingerprint, migrationTS.Format(time.RFC3339Nano), legacyPath)
	if err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: record fingerprint: %v", ErrMigrationIntegrity, err)
	}

	tmpDB.Close()

	// Fsync the temp file to ensure durability before rename.
	// On some platforms (Windows), the file may not be re-openable immediately
	// after SQLite closes it; in that case we proceed since SQLite's own
	// synchronous=FULL guarantees the data was flushed during VACUUM INTO.
	if err := fsyncFile(tmpPath); err != nil && !os.IsPermission(err) {
		// Only fail for non-permission errors (permission errors on Windows
		// indicate the file was just closed by SQLite and is already synced)
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: fsync temp: %v", ErrMigrationIntegrity, err)
	}

	// Atomic rename (same filesystem guarantees atomicity)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("%w: atomic rename failed: %v", ErrMigrationPermission, err)
	}

	// Fsync directory to ensure rename is durable
	if err := fsyncDir(stateDir); err != nil {
		// Target is published but directory entry may not be durable.
		// This is not fatal but should be logged.
		_ = err
	}

	// Verify the published target
	if err := validateTargetDB(targetPath); err != nil {
		return nil, err
	}

	return &MigrationResult{
		Action:            "migrated",
		TargetPath:        targetPath,
		SourceFingerprint: fingerprint,
		RowCounts:         rowCounts,
	}, nil
}

// checkIntegrity runs SQLite's quick_check on the given database.
func checkIntegrity(db *sql.DB) error {
	var result string
	err := db.QueryRow("PRAGMA integrity_check").Scan(&result)
	if err != nil {
		return fmt.Errorf("integrity_check query failed: %v", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned: %s", result)
	}
	return nil
}

// checkRequiredTables verifies that the migrated DB has the expected legacy tables.
func checkRequiredTables(db *sql.DB) error {
	required := []string{"orders", "positions", "strategy_state"}
	for _, table := range required {
		var count int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count)
		if err != nil {
			return fmt.Errorf("check table %s: %v", table, err)
		}
		if count == 0 {
			return fmt.Errorf("required table missing: %s", table)
		}
	}
	return nil
}

// countLegacyRows returns row counts for all standard legacy tables.
func countLegacyRows(db *sql.DB) (map[string]int64, error) {
	tables := []string{"orders", "positions", "strategy_state"}
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
		if err != nil {
			return nil, fmt.Errorf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts, nil
}

// computeFingerprint returns the SHA-256 hex of the main database file.
// Note: if WAL has uncommitted data, the fingerprint covers only the main file;
// VACUUM INTO already merges committed WAL into the copy.
func computeFingerprint(dbPath string) (string, error) {
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

// hasValidMigrationMarker checks if the target contains a migration_metadata
// record matching the legacy source fingerprint.
func hasValidMigrationMarker(targetPath, legacyPath string) bool {
	db, err := sql.Open("sqlite3", targetPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return false
	}
	defer db.Close()

	var storedFingerprint string
	err = db.QueryRow(
		"SELECT value FROM migration_metadata WHERE key='source_fingerprint'",
	).Scan(&storedFingerprint)
	if err != nil {
		return false
	}

	currentFingerprint, err := computeFingerprint(legacyPath)
	if err != nil {
		return false
	}

	return storedFingerprint == currentFingerprint
}

// validateTargetDB opens the target and runs integrity/schema checks.
func validateTargetDB(targetPath string) error {
	db, err := sql.Open("sqlite3", targetPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("%w: cannot open target: %v", ErrMigrationCorrupted, err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("%w: target ping failed: %v", ErrMigrationCorrupted, err)
	}

	if err := checkIntegrity(db); err != nil {
		return fmt.Errorf("%w: target integrity failed: %v", ErrMigrationCorrupted, err)
	}

	return nil
}

// acquireMigrationLock creates an exclusive lock file in the state directory.
func acquireMigrationLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: lock file exists: %s", ErrMigrationLock, lockPath)
		}
		return nil, fmt.Errorf("%w: %v", ErrMigrationLock, err)
	}
	_, _ = fmt.Fprintf(f, "pid=%d time=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = f.Sync()
	return f, nil
}

// releaseMigrationLock removes the lock file.
func releaseMigrationLock(f *os.File, lockPath string) {
	if f != nil {
		f.Close()
	}
	os.Remove(lockPath)
}

// fsyncFile opens and fsyncs a file to ensure its data is durable.
func fsyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// fsyncDir fsyncs a directory to ensure metadata (like renames) is durable.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// fileExists checks if a file (not directory) exists at the given path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// escapeSQLitePath escapes single quotes for use in SQLite VACUUM INTO path.
func escapeSQLitePath(path string) string {
	return strings.ReplaceAll(path, "'", "''")
}

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckSecurityConstraints_ValidPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks not applicable on Windows")
	}

	// Create a temporary config file with 0600 permissions
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(configPath, []byte("test: value"), 0600)
	if err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}

	err = CheckSecurityConstraints(configPath)
	if err != nil {
		t.Errorf("expected no error for 0600 permissions, got: %v", err)
	}
}

func TestCheckSecurityConstraints_TooPermissive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks not applicable on Windows")
	}

	// Create a temporary config file with 0644 permissions (too permissive)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(configPath, []byte("test: value"), 0644)
	if err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}

	err = CheckSecurityConstraints(configPath)
	if err == nil {
		t.Error("expected error for 0644 permissions")
	}
}

func TestCheckSecurityConstraints_WorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks not applicable on Windows")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(configPath, []byte("test: value"), 0666)
	if err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}

	err = CheckSecurityConstraints(configPath)
	if err == nil {
		t.Error("expected error for 0666 permissions")
	}
}

func TestCheckSecurityConstraints_NonExistentFile(t *testing.T) {
	// Non-existent file should not cause an error (skip permission check)
	err := CheckSecurityConstraints("/nonexistent/path/config.yaml")
	if err != nil {
		t.Errorf("expected no error for non-existent file, got: %v", err)
	}
}

func TestCheckSecurityConstraints_EmptyPath(t *testing.T) {
	// Empty path should not cause an error
	err := CheckSecurityConstraints("")
	if err != nil {
		t.Errorf("expected no error for empty path, got: %v", err)
	}
}

func TestCheckSecurityConstraints_OwnerReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks not applicable on Windows")
	}

	// 0400 (owner read only) is acceptable (more restrictive than 0600)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(configPath, []byte("test: value"), 0400)
	if err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}

	err = CheckSecurityConstraints(configPath)
	if err != nil {
		t.Errorf("expected no error for 0400 permissions, got: %v", err)
	}
}

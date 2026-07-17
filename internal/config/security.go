package config

import (
	"fmt"
	"os"
	"runtime"
)

// CheckSecurityConstraints verifies security constraints before startup:
//   - The process is not running as root (Linux only)
//   - The config file permissions are no more permissive than 0600
//
// Returns an error with a specific violation message if any constraint is violated.
func CheckSecurityConstraints(configFilePath string) error {
	// Check if running as root (only applicable on Linux/Unix)
	if runtime.GOOS != "windows" {
		if os.Getuid() == 0 {
			return fmt.Errorf("security violation: process is running as root; the system refuses to start as root user")
		}
	}

	// Check config file permissions
	if configFilePath != "" {
		info, err := os.Stat(configFilePath)
		if err != nil {
			if os.IsNotExist(err) {
				// If the config file doesn't exist, skip permission check
				return nil
			}
			return fmt.Errorf("security check: unable to stat config file %q: %w", configFilePath, err)
		}

		// Only check permissions on non-Windows systems
		if runtime.GOOS != "windows" {
			mode := info.Mode().Perm()
			// Check if permissions are more permissive than 0600
			// 0600 = owner read+write only; anything beyond that is too permissive
			if mode&0o177 != 0 {
				return fmt.Errorf("security violation: config file %q has permissions %04o, which is more permissive than 0600; please run: chmod 600 %s", configFilePath, mode, configFilePath)
			}
		}
	}

	return nil
}

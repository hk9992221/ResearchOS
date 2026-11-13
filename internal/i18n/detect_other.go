//go:build !windows

package i18n

import (
	"os"
	"strings"
)

// detectLocale reads the POSIX locale environment in its precedence order:
// LC_ALL, then LC_MESSAGES, then LANG. macOS and the Linux desktops set the
// same variables.
func detectLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

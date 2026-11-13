//go:build windows

package i18n

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultLocaleName = kernel32.NewProc("GetUserDefaultLocaleName")
	procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")
)

// detectLocale returns the language the user chose for Windows itself, which
// is what "follow the system" must follow: the first of the user's preferred
// UI languages, then the user default locale, then the user UI language id.
func detectLocale() string {
	if langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil {
		for _, lang := range langs {
			if lang != "" {
				return lang
			}
		}
	}
	if name := userDefaultLocaleName(); name != "" {
		return name
	}
	if id, _, _ := procGetUserDefaultUILanguage.Call(); id != 0 {
		switch id & 0x3ff {
		case 0x04: // LANG_CHINESE
			return "zh"
		case 0x09: // LANG_ENGLISH
			return "en"
		}
	}
	if env := os.Getenv("LANG"); env != "" {
		return env
	}
	return ""
}

// userDefaultLocaleName reads the user's default locale, as "zh-CN".
func userDefaultLocaleName() string {
	// LOCALE_NAME_MAX_LENGTH is 85: 84 characters and the terminator.
	buf := make([]uint16, 85)
	n, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 || n > uintptr(len(buf)) {
		return ""
	}
	return windows.UTF16ToString(buf[:n-1]) // the count includes the terminator
}

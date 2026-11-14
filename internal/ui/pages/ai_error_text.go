package pages

// AI failures cross the UI as aierr.AIError values. The settings page and every
// module that can start a generation share this one kind-to-message mapping so
// the same upstream failure reads the same everywhere.

import (
	"errors"

	"researchos/internal/ai/aierr"
	"researchos/internal/i18n"
	"researchos/internal/ui/errtext"
)

// aiErrorText localizes an AI failure. A typed AIError contributes its kind
// label plus the sanitized upstream detail; any other error keeps the shared
// errtext translation. Key-like material is redacted again before display.
func aiErrorText(err error) string {
	if err == nil {
		return ""
	}
	var ae *aierr.AIError
	if errors.As(err, &ae) {
		text := aiErrorKindText(string(ae.Kind))
		if ae.Message != "" {
			text += "：" + aierr.Redact(ae.Message)
		}
		return text
	}
	return aierr.Redact(errtext.Text(err))
}

// aiErrorKindText maps the stable ErrorKind values to the shared translations.
// Unknown kinds use a generic label instead of leaking the English identifier.
func aiErrorKindText(kind string) string {
	key := "pages.settings.ai.kind." + kind
	text := i18n.T(key)
	if text == key {
		return i18n.T("pages.settings.ai.kind.unknown")
	}
	return text
}

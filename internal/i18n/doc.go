/*
Package i18n translates the ResearchOS interface and selects the language it
shows.

# Language packs

A language pack is one embedded directory under locales/, named by its BCP 47
tag (en, zh). The directory name is the tag: there is no list of supported
languages anywhere in the code, so adding a pack directory is the whole step
that makes a language available.

	internal/i18n/locales/en/common.json
	internal/i18n/locales/en/settings.json
	internal/i18n/locales/en/errors.json
	internal/i18n/locales/zh/common.json
	internal/i18n/locales/zh/settings.json
	internal/i18n/locales/zh/errors.json

Each language is split into area files so a translator updates one area
without touching the rest, and so two translators never edit the same file.
The areas are:

	common.json    labels shared across the whole interface
	settings.json  the settings page
	errors.json    the sentences errtext shows for foundation errors

A pack is the merge of its area files. A duplicate key across the files of one
language is a programming error and panics at init with the file that holds
it; a missing key at runtime falls back instead (see below).

# Keys

Keys are flat strings namespaced by dots, area then purpose:

	language.name                 the pack's own name for itself
	settings.language.label       a label
	settings.language.saved       a sentence
	error.not_workspace           a foundation error sentinel

A pack MUST define "language.name" with the language's own name, as "English"
and "简体中文"; the settings selector shows it and it is never translated.

# Placeholders

A value may name placeholders in braces, as "Follow system ({language})".
Fill them with Tf:

	i18n.Tf("settings.language.system_hint", map[string]string{"language": "简体中文"})

A placeholder without a value is left in the text, which makes the omission
visible.

# Fallback

T resolves current language → English → the key itself. The English pack is
therefore required. Nothing here panics at runtime on a missing key, so string
extraction may land page by page without breaking the interface.

# Languages and locales

SetLanguage accepts the configuration values "" and "system" (follow the
operating system), a supported tag, or any locale whose base language has a
pack. The match is by base language: zh-CN, zh-SG, zh-Hans, zh-TW and zh-Hant
all resolve to zh. Only a Simplified pack exists today, so a Traditional
Chinese system reads Simplified Chinese; adding a zh-Hant pack and making
lookup prefer script variants is the later step.

The system locale comes from SystemLocale, a package variable over the
platform detector (the Windows user interface language, else the POSIX locale
environment). Tests and verification programs replace it to force a locale.

# Concurrency

SetLanguage stores a pointer to the selected pack atomically and T reads that
pointer: a click may switch the language while a frame is being built, and T
is a single map lookup with no lock and no allocation.
*/
package i18n

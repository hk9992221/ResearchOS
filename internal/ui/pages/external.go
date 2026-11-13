package pages

import (
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/i18n"
	"researchos/internal/ui/components"
)

// externalReasonLabel translates the closed set of Chinese reason strings the
// externalrefs probe returns (internal/foundation/externalrefs/service.go).
// Anything else is a raw operating-system error, already detail the caller
// added, and passes through unchanged.
func externalReasonLabel(reason string) string {
	switch reason {
	case "不存在":
		return i18n.T("pages.external.reason_missing")
	case "权限拒绝":
		return i18n.T("pages.external.reason_denied")
	case "不是目录":
		return i18n.T("pages.external.reason_not_folder")
	case "不是文件":
		return i18n.T("pages.external.reason_not_file")
	case "不是 git 仓库":
		return i18n.T("pages.external.reason_not_git")
	case "未知引用类型":
		return i18n.T("pages.external.reason_unknown_kind")
	}
	return reason
}

// ExternalWarning renders the inline warning of §16.6 for a broken external
// reference: the original path, the reason, and the relocate entry. A healthy
// reference renders nothing, so one broken folder never blocks the page.
func ExternalWarning(v *components.UI, st externalrefs.Status, onRelocate func()) {
	if st.Exists {
		return
	}
	message := i18n.Tf("pages.external.message", map[string]string{
		"path":   st.Path,
		"reason": externalReasonLabel(st.Reason),
	})
	if st.Reason == "" {
		message = st.Path
	}
	v.Banner(components.BannerOptions{
		Tone:        components.Warning,
		Title:       i18n.T("pages.external.title"),
		Message:     message,
		ActionLabel: i18n.T("pages.external.btn_relocate"),
		OnAction:    onRelocate,
	})
}

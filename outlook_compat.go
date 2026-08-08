package gsmail

import (
	"github.com/gsoultan/gsmail/outlook"
)

// The Outlook HTML builders now live in github.com/gsoultan/gsmail/outlook.
// They are a rendering concern with a different risk profile from mail
// transport — every one of them produces markup that becomes the template
// source for SetHTMLBody, where html/template's contextual escaping does not
// apply — so they get their own package and their own release cadence.
//
// The aliases below keep existing code compiling. They are deprecated and
// will be removed in v1; switch the import and drop the gsmail. prefix.
//
// Migration is mechanical:
//
//	gsmail.MSOButton(cfg)  ->  outlook.MSOButton(cfg)

// ButtonConfig configures MSOButton.
//
// Deprecated: use outlook.ButtonConfig.
type ButtonConfig = outlook.ButtonConfig

// MSOPreheaderMaxLength is the recommended maximum preheader length.
//
// Deprecated: use outlook.MSOPreheaderMaxLength.
const MSOPreheaderMaxLength = outlook.MSOPreheaderMaxLength

// ToOutlookHTML rewrites HTML for Outlook compatibility.
//
// Deprecated: use outlook.ToOutlookHTML.
func ToOutlookHTML(html []byte) []byte { return outlook.ToOutlookHTML(html) }

// IsOutlookCompatible reports whether the HTML already carries Outlook fixes.
//
// Deprecated: use outlook.IsOutlookCompatible.
func IsOutlookCompatible(html []byte) bool { return outlook.IsOutlookCompatible(html) }

// MSOTable builds a normalised table with Outlook fixes.
//
// Deprecated: use outlook.MSOTable.
func MSOTable(width, align, style, content string) string {
	return outlook.MSOTable(width, align, style, content)
}

// MSOSpacer builds a vertical spacer.
//
// Deprecated: use outlook.MSOSpacer.
func MSOSpacer(height int) string { return outlook.MSOSpacer(height) }

// WrapInGhostTable wraps content in an MSO conditional table.
//
// Deprecated: use outlook.WrapInGhostTable.
func WrapInGhostTable(html, width, align string) string {
	return outlook.WrapInGhostTable(html, width, align)
}

// MSOOnly renders content visible only in Outlook.
//
// Deprecated: use outlook.MSOOnly.
func MSOOnly(html string) string { return outlook.MSOOnly(html) }

// HideFromMSO renders content hidden from Outlook.
//
// Deprecated: use outlook.HideFromMSO.
func HideFromMSO(html string) string { return outlook.HideFromMSO(html) }

// MSOPreheader builds hidden inbox-preview text.
//
// Deprecated: use outlook.MSOPreheader.
func MSOPreheader(text string) string { return outlook.MSOPreheader(text) }

// MSOPreheaderTruncated builds preheader text truncated to maxLen runes.
//
// Deprecated: use outlook.MSOPreheaderTruncated.
func MSOPreheaderTruncated(text string, maxLen int) string {
	return outlook.MSOPreheaderTruncated(text, maxLen)
}

// MSOEmoji wraps emoji in an Outlook-safe font span.
//
// Deprecated: use outlook.MSOEmoji.
func MSOEmoji(text string) string { return outlook.MSOEmoji(text) }

// MSOSafeFontStack returns an Outlook-safe font stack.
//
// Deprecated: use outlook.MSOSafeFontStack.
func MSOSafeFontStack() string { return outlook.MSOSafeFontStack() }

// MSOEmailLayout builds a standard Outlook-compatible email structure.
//
// Deprecated: use outlook.MSOEmailLayout.
func MSOEmailLayout(width int, preheader, header, body, footer string) string {
	return outlook.MSOEmailLayout(width, preheader, header, body, footer)
}

// MSOButton builds a bulletproof VML button.
//
// Deprecated: use outlook.MSOButton.
func MSOButton(cfg ButtonConfig) string { return outlook.MSOButton(cfg) }

// MSOImage builds an image tag with Outlook fixes.
//
// Deprecated: use outlook.MSOImage.
func MSOImage(src, alt string, width, height int, style string) string {
	return outlook.MSOImage(src, alt, width, height, style)
}

// MSOFontStack builds a quoted font stack.
//
// Deprecated: use outlook.MSOFontStack.
func MSOFontStack(fonts ...string) string { return outlook.MSOFontStack(fonts...) }

// MSOBackground builds a VML background.
//
// Deprecated: use outlook.MSOBackground.
func MSOBackground(url, color string, width, height int, content string) string {
	return outlook.MSOBackground(url, color, width, height, content)
}

// MSOColumns builds side-by-side columns using ghost tables.
//
// Deprecated: use outlook.MSOColumns.
func MSOColumns(widths []int, cols ...string) string { return outlook.MSOColumns(widths, cols...) }

// MSOBulletList builds an Outlook-safe bulleted list.
//
// Deprecated: use outlook.MSOBulletList.
func MSOBulletList(items []string, bullet, style string) string {
	return outlook.MSOBulletList(items, bullet, style)
}

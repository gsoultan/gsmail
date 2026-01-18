package gsmail

import (
	"bytes"
	"fmt"
)

var (
	tagHtmlLower = []byte("<html")
	tagHtmlUpper = []byte("<HTML")
	tagHeadLower = []byte("<head")
	tagHeadUpper = []byte("<HEAD")
	tagBodyLower = []byte("<body")
	tagBodyUpper = []byte("<BODY")
	tagTable     = []byte("<table")
	tagImg       = []byte("<img")

	outlookNamespaces = []byte(` xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:w="urn:schemas-microsoft-com:office:word" xmlns:m="http://schemas.microsoft.com/office/2004/12/omml"`)
	outlookHeadTags   = []byte(`
    <!--[if gte mso 9]>
    <xml>
        <o:OfficeDocumentSettings>
            <o:AllowPNG/>
            <o:PixelsPerInch>96</o:PixelsPerInch>
        </o:OfficeDocumentSettings>
    </xml>
    <![endif]-->
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="format-detection" content="telephone=no, date=no, address=no, email=no">
    <meta name="x-apple-disable-message-reformatting">
    <meta name="color-scheme" content="light dark">
    <meta name="supported-color-schemes" content="light dark">
    <style type="text/css">
        :root {
            color-scheme: light dark;
            supported-color-schemes: light dark;
        }
        body, table, td, p, a { -ms-text-size-adjust: 100%; -webkit-text-size-adjust: 100%; }
        table, td { mso-table-lspace: 0pt; mso-table-rspace: 0pt; }
        img { -ms-interpolation-mode: bicubic; }
        img { border: 0; height: auto; line-height: 100%; outline: none; text-decoration: none; }
        table { border-collapse: collapse !important; }
        body { height: 100% !important; margin: 0 !important; padding: 0 !important; width: 100% !important; }
        p { margin: 1em 0; }
        td, p { mso-line-height-rule: exactly; }
        h1, h2, h3, h4, h5, h6 { display: block; margin: 0; padding: 0; }
        .ExternalClass { width: 100%; }
        .ExternalClass, .ExternalClass p, .ExternalClass span, .ExternalClass font, .ExternalClass td, .ExternalClass div { line-height: 100%; }
        #OutlookHolder { padding: 0; }
        
        /* Link fixes */
        a[x-apple-data-detectors] {
            color: inherit !important;
            text-decoration: none !important;
            font-size: inherit !important;
            font-family: inherit !important;
            font-weight: inherit !important;
            line-height: inherit !important;
        }
        span.MsoHyperlink { mso-style-priority: 99; color: inherit; }
        span.MsoHyperlinkFollowed { mso-style-priority: 99; color: inherit; }

        /* Font scaling fix */
        @media only screen and (min-width: 600px) {
            .templateContainer { width: 600px !important; }
        }
        /* Dark Mode */
        @media (prefers-color-scheme: dark) {
            .dark-mode-bg { background-color: #2d2d2d !important; }
            .dark-mode-text { color: #ffffff !important; }
        }
        [data-ogsc] .dark-mode-bg { background-color: #2d2d2d !important; }
        [data-ogsc] .dark-mode-text { color: #ffffff !important; }
    </style>
    <!--[if gte mso 9]>
    <style type="text/css">
        li { text-indent: -1em; } /* Fix list indentation in Outlook */
        table { border-collapse: collapse; }
    </style>
    <![endif]-->
`)
)

// IsOutlookCompatible checks if the given HTML byte slice contains markers of an Outlook-compatible template.
func IsOutlookCompatible(html []byte) bool {
	if len(html) == 0 {
		return false
	}
	// Check for VML namespace or Office namespaces which are strong indicators of Outlook-specific fixes,
	// or specific MSO styles we inject.
	return bytes.Contains(html, []byte("urn:schemas-microsoft-com:vml")) ||
		bytes.Contains(html, []byte("urn:schemas-microsoft-com:office:office")) ||
		bytes.Contains(html, []byte("mso-line-height-rule: exactly")) ||
		bytes.Contains(html, []byte("<!--[if gte mso 9]>"))
}

// ToOutlookHTML converts an HTML email template to be compatible with Microsoft Outlook.
// It injects necessary namespaces, meta tags, and MSO-specific styles.
func ToOutlookHTML(html []byte) []byte {
	if len(html) == 0 {
		return html
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	htmlIdx := findTag(html, tagHtmlLower, tagHtmlUpper)
	headIdx := findTag(html, tagHeadLower, tagHeadUpper)
	bodyIdx := findTag(html, tagBodyLower, tagBodyUpper)

	if htmlIdx == -1 && headIdx == -1 {
		// Fragment: Wrap in full structure with container table
		*bufPtr = append(*bufPtr, []byte(`<!DOCTYPE html><html><head>`)...)
		*bufPtr = append(*bufPtr, outlookHeadTags...)
		*bufPtr = append(*bufPtr, []byte(`</head><body id="OutlookHolder"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr><td>`)...)
		appendNormalized(bufPtr, html)
		*bufPtr = append(*bufPtr, []byte(`</td></tr></table></body></html>`)...)
	} else {
		curr := 0
		if htmlIdx != -1 {
			// Find end of <html tag
			htmlEnd := bytes.IndexByte(html[htmlIdx:], '>')
			if htmlEnd != -1 {
				htmlEnd += htmlIdx
				*bufPtr = append(*bufPtr, html[curr:htmlEnd]...)
				*bufPtr = append(*bufPtr, outlookNamespaces...)
				*bufPtr = append(*bufPtr, '>')
				curr = htmlEnd + 1
			}
		}

		if headIdx != -1 {
			headEnd := bytes.IndexByte(html[headIdx:], '>')
			if headEnd != -1 {
				headEnd += headIdx
				*bufPtr = append(*bufPtr, html[curr:headEnd+1]...)
				*bufPtr = append(*bufPtr, outlookHeadTags...)
				curr = headEnd + 1
			}
		} else if htmlIdx != -1 {
			*bufPtr = append(*bufPtr, []byte("\n<head>")...)
			*bufPtr = append(*bufPtr, outlookHeadTags...)
			*bufPtr = append(*bufPtr, []byte("</head>")...)
		}

		// Body handling: wrap content in a container table for better Outlook rendering
		if bodyIdx != -1 && bodyIdx >= curr {
			bodyEnd := bytes.IndexByte(html[bodyIdx:], '>')
			if bodyEnd != -1 {
				bodyEnd += bodyIdx
				*bufPtr = append(*bufPtr, html[curr:bodyEnd+1]...)
				*bufPtr = append(*bufPtr, []byte(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr><td>`)...)
				curr = bodyEnd + 1

				bodyCloseIdx := bytes.Index(html[curr:], []byte("</body>"))
				if bodyCloseIdx == -1 {
					bodyCloseIdx = bytes.Index(html[curr:], []byte("</BODY>"))
				}

				if bodyCloseIdx != -1 {
					bodyCloseIdx += curr
					appendNormalized(bufPtr, html[curr:bodyCloseIdx])
					*bufPtr = append(*bufPtr, []byte(`</td></tr></table>`)...)
					curr = bodyCloseIdx
				}
			}
		}

		// Rest of content
		appendNormalized(bufPtr, html[curr:])
	}

	res := make([]byte, len(*bufPtr))
	copy(res, *bufPtr)
	return res
}

func findTag(html, lower, upper []byte) int {
	idx := bytes.Index(html, lower)
	if idx == -1 {
		idx = bytes.Index(html, upper)
	}
	return idx
}

func isTableTag(tag []byte) bool {
	if len(tag) < 7 {
		return false
	}
	return (tag[1] == 't' || tag[1] == 'T') &&
		(tag[2] == 'a' || tag[2] == 'A') &&
		(tag[3] == 'b' || tag[3] == 'B') &&
		(tag[4] == 'l' || tag[4] == 'L') &&
		(tag[5] == 'e' || tag[5] == 'E') &&
		(tag[6] == ' ' || tag[6] == '>')
}

func isImgTag(tag []byte) bool {
	if len(tag) < 5 {
		return false
	}
	return (tag[1] == 'i' || tag[1] == 'I') &&
		(tag[2] == 'm' || tag[2] == 'M') &&
		(tag[3] == 'g' || tag[3] == 'G') &&
		(tag[4] == ' ' || tag[4] == '>')
}

func appendNormalized(dest *[]byte, src []byte) {
	curr := 0
	for curr < len(src) {
		idx := bytes.IndexByte(src[curr:], '<')
		if idx == -1 {
			*dest = append(*dest, src[curr:]...)
			break
		}
		idx += curr
		*dest = append(*dest, src[curr:idx]...)

		// Skip comments
		if bytes.HasPrefix(src[idx:], []byte("<!--")) {
			end := bytes.Index(src[idx:], []byte("-->"))
			if end != -1 {
				end += idx + 3
				*dest = append(*dest, src[idx:end]...)
				curr = end
				continue
			}
		}

		end := bytes.IndexByte(src[idx:], '>')
		if end == -1 {
			*dest = append(*dest, src[idx:]...)
			break
		}
		end += idx

		tag := src[idx : end+1]
		if isTableTag(tag) && !bytes.Contains(tag, []byte("role=")) {
			closing := end
			if src[end-1] == '/' {
				closing--
			}
			*dest = append(*dest, src[idx:closing]...)
			*dest = append(*dest, []byte(` role="presentation" cellspacing="0" cellpadding="0" border="0"`)...)
			*dest = append(*dest, src[closing:end+1]...)
		} else if isImgTag(tag) && !bytes.Contains(tag, []byte("border=")) {
			closing := end
			if src[end-1] == '/' {
				closing--
			}
			*dest = append(*dest, src[idx:closing]...)
			*dest = append(*dest, []byte(` border="0"`)...)
			*dest = append(*dest, src[closing:end+1]...)
		} else {
			*dest = append(*dest, tag...)
		}
		curr = end + 1
	}
}

// MSOTable generates a normalized table for Outlook with standard email-safe attributes.
func MSOTable(width, align, style, content string) string {
	if align == "" {
		align = "left"
	}
	if width == "" {
		width = "100%"
	}
	return fmt.Sprintf(`<table role="presentation" cellspacing="0" cellpadding="0" border="0" width="%s" align="%s" style="%s"><tr><td>%s</td></tr></table>`,
		width, align, style, content)
}

// MSOSpacer generates an Outlook-compatible vertical spacer.
func MSOSpacer(height int) string {
	if height <= 0 {
		return ""
	}
	hStr := fmt.Sprintf("%d", height)
	return fmt.Sprintf(`<!--[if mso]>
    <table role="presentation" cellspacing="0" cellpadding="0" border="0" width="100%%">
    <tr>
    <td height="%s" style="font-size:%spx; line-height:%spx;">&nbsp;</td>
    </tr>
    </table>
    <![endif]-->
    <div style="height:%spx; line-height:%spx; font-size:1px;">&nbsp;</div>`, hStr, hStr, hStr, hStr, hStr)
}

// WrapInGhostTable wraps the given HTML in an Outlook MSO conditional table (ghost table).
// This is useful for enforcing widths on elements that Outlook otherwise ignores (like div width).
func WrapInGhostTable(html string, width string, align string) string {
	if align == "" {
		align = "center"
	}
	return `<!--[if mso]>
    <table role="presentation" width="` + width + `" cellspacing="0" cellpadding="0" border="0" align="` + align + `">
    <tr>
    <td>
    <![endif]-->
    ` + html + `
    <!--[if mso]>
    </td>
    </tr>
    </table>
    <![endif]-->`
}

// MSOOnly wraps the given HTML to only be visible in Microsoft Outlook.
func MSOOnly(html string) string {
	return `<!--[if mso]>` + html + `<![endif]-->`
}

// HideFromMSO wraps the given HTML to be hidden from Microsoft Outlook.
func HideFromMSO(html string) string {
	return `<!--[if !mso]><!-->` + html + `<!--<![endif]-->`
}

// ButtonConfig represents the configuration for an Outlook-compatible button.
type ButtonConfig struct {
	Text         string
	Link         string
	Width        int
	Height       int
	Color        string
	BgColor      string
	BorderRadius int
	FontSize     int
	FontFamily   string
	FontWeight   string
}

// MSOButton generates a "bulletproof" button compatible with Microsoft Outlook using VML.
func MSOButton(cfg ButtonConfig) string {
	if cfg.Color == "" {
		cfg.Color = "#ffffff"
	}
	if cfg.BgColor == "" {
		cfg.BgColor = "#007bff"
	}
	if cfg.FontFamily == "" {
		cfg.FontFamily = "sans-serif"
	}
	if cfg.FontSize == 0 {
		cfg.FontSize = 16
	}
	if cfg.FontWeight == "" {
		cfg.FontWeight = "bold"
	}
	if cfg.Width == 0 {
		cfg.Width = 200
	}
	if cfg.Height == 0 {
		cfg.Height = 40
	}

	widthStr := fmt.Sprintf("%dpx", cfg.Width)
	heightStr := fmt.Sprintf("%dpx", cfg.Height)
	fontSizeStr := fmt.Sprintf("%dpx", cfg.FontSize)

	// VML arcsize is a percentage of the smaller dimension.
	// For simplicity, we approximate it here.
	arcsize := 0
	if cfg.Height > 0 && cfg.BorderRadius > 0 {
		arcsize = (cfg.BorderRadius * 100) / cfg.Height
		if arcsize > 100 {
			arcsize = 100
		}
	}

	return fmt.Sprintf(`<div><!--[if mso]>
    <v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="%s" style="height:%s;v-text-anchor:middle;width:%s;" arcsize="%d%%" stroke="f" fillcolor="%s">
    <w:anchorlock/>
    <center>
    <![endif]-->
    <a href="%s" style="background-color:%s;border-radius:%dpx;color:%s;display:inline-block;font-family:%s;font-size:%s;font-weight:%s;line-height:%s;text-align:center;text-decoration:none;width:%s;-webkit-text-size-adjust:none;">%s</a>
    <!--[if mso]>
    </center>
    </v:roundrect>
    <![endif]--></div>`,
		cfg.Link, heightStr, widthStr, arcsize, cfg.BgColor,
		cfg.Link, cfg.BgColor, cfg.BorderRadius, cfg.Color, cfg.FontFamily, fontSizeStr, cfg.FontWeight, heightStr, widthStr, cfg.Text)
}

// MSOImage generates an image tag with Outlook-specific fixes.
func MSOImage(src, alt string, width, height int, style string) string {
	wAttr := ""
	if width > 0 {
		wAttr = fmt.Sprintf(` width="%d"`, width)
	}
	hAttr := ""
	if height > 0 {
		hAttr = fmt.Sprintf(` height="%d"`, height)
	}
	return fmt.Sprintf(`<img src="%s" alt="%s"%s%s style="display:block; border:0; outline:none; text-decoration:none; -ms-interpolation-mode:bicubic; %s">`,
		src, alt, wAttr, hAttr, style)
}

// MSOFontStack returns a font stack string that ensures better fallback in Outlook.
func MSOFontStack(fonts ...string) string {
	if len(fonts) == 0 {
		return "sans-serif"
	}
	res := ""
	for i, f := range fonts {
		if i > 0 {
			res += ", "
		}
		if bytes.Contains([]byte(f), []byte(" ")) {
			res += "'" + f + "'"
		} else {
			res += f
		}
	}
	return res
}

// MSOBackground generates a VML-based background image/color for Outlook compatibility.
// It wraps the provided content in a VML rectangle with the specified background.
func MSOBackground(url string, color string, width, height int, content string) string {
	widthStr := "600px"
	if width > 0 {
		widthStr = fmt.Sprintf("%dpx", width)
	}
	heightStr := "400px"
	if height > 0 {
		heightStr = fmt.Sprintf("%dpx", height)
	}

	fill := ""
	if url != "" {
		fill = fmt.Sprintf(`<v:fill type="tile" src="%s" color="%s" />`, url, color)
	} else {
		fill = fmt.Sprintf(`<v:fill color="%s" />`, color)
	}

	return fmt.Sprintf(`
<div style="background-color:%s; background-image:url('%s'); background-position:center; background-repeat:no-repeat; background-size:cover;">
  <!--[if gte mso 9]>
  <v:rect xmlns:v="urn:schemas-microsoft-com:vml" fill="true" stroke="false" style="width:%s;height:%s;">
    %s
    <v:textbox inset="0,0,0,0">
  <![endif]-->
  <div>
    %s
  </div>
  <!--[if gte mso 9]>
    </v:textbox>
  </v:rect>
  <![endif]-->
</div>`, color, url, widthStr, heightStr, fill, content)
}

// MSOColumns wraps multiple HTML fragments into Outlook-compatible side-by-side columns using ghost tables.
// widths specifies the width of each column in pixels.
func MSOColumns(widths []int, cols ...string) string {
	if len(cols) == 0 {
		return ""
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	// Fallback for non-Outlook: flex or inline-block
	*bufPtr = append(*bufPtr, []byte(`<div style="font-size:0; text-align:center;">`)...)

	// Outlook Ghost Table Start
	*bufPtr = append(*bufPtr, []byte(`<!--[if mso]><table role="presentation" border="0" cellspacing="0" cellpadding="0" width="100%"><tr><![endif]-->`)...)

	for i, col := range cols {
		w := 0
		if i < len(widths) {
			w = widths[i]
		}

		wStr := ""
		if w > 0 {
			wStr = fmt.Sprintf("width:%dpx;", w)
		}

		// Outlook Cell Start
		if w > 0 {
			*bufPtr = append(*bufPtr, []byte(fmt.Sprintf(`<!--[if mso]><td style="width:%dpx;" valign="top"><![endif]-->`, w))...)
		} else {
			*bufPtr = append(*bufPtr, []byte(`<!--[if mso]><td valign="top"><![endif]-->`)...)
		}

		// Responsive Column wrapper
		*bufPtr = append(*bufPtr, []byte(fmt.Sprintf(`<div style="display:inline-block; %s vertical-align:top; font-size:16px; width:100%%; max-width:%dpx;">`, wStr, w))...)
		*bufPtr = append(*bufPtr, []byte(col)...)
		*bufPtr = append(*bufPtr, []byte(`</div>`)...)

		// Outlook Cell End
		*bufPtr = append(*bufPtr, []byte(`<!--[if mso]></td><![endif]-->`)...)
	}

	// Outlook Ghost Table End
	*bufPtr = append(*bufPtr, []byte(`<!--[if mso]></tr></table><![endif]-->`)...)
	*bufPtr = append(*bufPtr, []byte(`</div>`)...)

	res := make([]byte, len(*bufPtr))
	copy(res, *bufPtr)
	return UnsafeBytesToString(res)
}

// MSOBulletList generates a consistent bulleted list that renders well in Outlook by avoiding native <ul> tags.
func MSOBulletList(items []string, bullet string, style string) string {
	if len(items) == 0 {
		return ""
	}
	if bullet == "" {
		bullet = "&#8226;" // Standard bullet
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	*bufPtr = append(*bufPtr, []byte(`<table role="presentation" border="0" cellspacing="0" cellpadding="0">`)...)
	for _, item := range items {
		*bufPtr = append(*bufPtr, []byte(`<tr>`)...)
		*bufPtr = append(*bufPtr, []byte(fmt.Sprintf(`<td style="padding:0 10px 0 0; vertical-align:top; %s">%s</td>`, style, bullet))...)
		*bufPtr = append(*bufPtr, []byte(fmt.Sprintf(`<td style="vertical-align:top; %s">%s</td>`, style, item))...)
		*bufPtr = append(*bufPtr, []byte(`</tr>`)...)
	}
	*bufPtr = append(*bufPtr, []byte(`</table>`)...)

	res := make([]byte, len(*bufPtr))
	copy(res, *bufPtr)
	return UnsafeBytesToString(res)
}

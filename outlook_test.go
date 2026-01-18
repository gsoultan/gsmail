package gsmail

import (
	"bytes"
	"testing"
)

func TestIsOutlookCompatible(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		expected bool
	}{
		{
			name:     "Empty",
			html:     "",
			expected: false,
		},
		{
			name:     "Plain HTML",
			html:     "<html><body>Hello</body></html>",
			expected: false,
		},
		{
			name:     "VML Namespace",
			html:     `<html xmlns:v="urn:schemas-microsoft-com:vml"><body>Hello</body></html>`,
			expected: true,
		},
		{
			name:     "Office Namespace",
			html:     `<html xmlns:o="urn:schemas-microsoft-com:office:office"><body>Hello</body></html>`,
			expected: true,
		},
		{
			name:     "MSO Style",
			html:     `<html><head><style>td { mso-line-height-rule: exactly; }</style></head><body>Hello</body></html>`,
			expected: true,
		},
		{
			name:     "MSO Conditional",
			html:     `<html><body><!--[if gte mso 9]>Only Outlook<![endif]--></body></html>`,
			expected: true,
		},
		{
			name:     "After ToOutlookHTML",
			html:     string(ToOutlookHTML([]byte("<html><body>Hello</body></html>"))),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOutlookCompatible([]byte(tt.html))
			if got != tt.expected {
				t.Errorf("IsOutlookCompatible() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEmail_IsOutlookCompatible(t *testing.T) {
	t.Run("Flag Set", func(t *testing.T) {
		email := &Email{OutlookCompatible: true}
		if !email.IsOutlookCompatible() {
			t.Error("Expected true when OutlookCompatible flag is set")
		}
	})

	t.Run("Flag Not Set, No Markers", func(t *testing.T) {
		email := &Email{Body: []byte("<html><body>Hello</body></html>")}
		if email.IsOutlookCompatible() {
			t.Error("Expected false when flag not set and no markers")
		}
	})

	t.Run("Flag Not Set, With Markers", func(t *testing.T) {
		email := &Email{Body: []byte(`<html xmlns:v="urn:schemas-microsoft-com:vml"><body>Hello</body></html>`)}
		if !email.IsOutlookCompatible() {
			t.Error("Expected true when markers are present even if flag not set")
		}
	})

	t.Run("After SetOutlookBody", func(t *testing.T) {
		email := &Email{}
		email.SetOutlookBody("<html><body>Hello</body></html>", nil)
		if !email.IsOutlookCompatible() {
			t.Error("Expected true after SetOutlookBody")
		}
	})
}

func TestToOutlookHTML(t *testing.T) {
	input := []byte(`<!DOCTYPE html>
<html>
<head>
    <title>Test Email</title>
</head>
<body>
    <h1>Hello, World!</h1>
</body>
</html>`)

	output := ToOutlookHTML(input)

	// Check for namespaces
	if !bytes.Contains(output, []byte(`xmlns:v="urn:schemas-microsoft-com:vml"`)) {
		t.Error("Missing xmlns:v namespace")
	}
	if !bytes.Contains(output, []byte(`xmlns:o="urn:schemas-microsoft-com:office:office"`)) {
		t.Error("Missing xmlns:o namespace")
	}
	if !bytes.Contains(output, []byte(`xmlns:w="urn:schemas-microsoft-com:office:word"`)) {
		t.Error("Missing xmlns:w namespace")
	}

	// Check for MSO settings
	if !bytes.Contains(output, []byte(`<o:OfficeDocumentSettings>`)) {
		t.Error("Missing o:OfficeDocumentSettings")
	}

	// Check for meta tags
	if !bytes.Contains(output, []byte(`http-equiv="X-UA-Compatible"`)) {
		t.Error("Missing X-UA-Compatible meta tag")
	}
	if !bytes.Contains(output, []byte(`name="format-detection"`)) {
		t.Error("Missing format-detection meta tag")
	}
	if !bytes.Contains(output, []byte(`name="color-scheme"`)) {
		t.Error("Missing color-scheme meta tag")
	}
	if !bytes.Contains(output, []byte(`color-scheme: light dark;`)) {
		t.Error("Missing color-scheme CSS")
	}

	// Check for Outlook CSS
	if !bytes.Contains(output, []byte(`mso-table-lspace: 0pt;`)) {
		t.Error("Missing Outlook CSS fixes")
	}
	if !bytes.Contains(output, []byte(`mso-line-height-rule: exactly;`)) {
		t.Error("Missing mso-line-height-rule")
	}
}

func TestToOutlookHTML_NoHead(t *testing.T) {
	input := []byte(`<html><body>Test</body></html>`)
	output := ToOutlookHTML(input)
	if !bytes.Contains(output, []byte(`<head>`)) {
		t.Error("Should have injected <head>")
	}
	if !bytes.Contains(output, []byte(`<o:OfficeDocumentSettings>`)) {
		t.Error("Missing o:OfficeDocumentSettings in injected head")
	}
}

func TestToOutlookHTML_Normalization(t *testing.T) {
	t.Run("Table Normalization", func(t *testing.T) {
		input := []byte(`<table><tr><td>Test</td></tr></table>`)
		output := ToOutlookHTML(input)
		if !bytes.Contains(output, []byte(`role="presentation"`)) {
			t.Error("Missing role=\"presentation\" in normalized table")
		}
		if !bytes.Contains(output, []byte(`cellspacing="0"`)) {
			t.Error("Missing cellspacing=\"0\" in normalized table")
		}
	})

	t.Run("Image Normalization", func(t *testing.T) {
		input := []byte(`<img src="test.png">`)
		output := ToOutlookHTML(input)
		if !bytes.Contains(output, []byte(`border="0"`)) {
			t.Error("Missing border=\"0\" in normalized image")
		}
	})

	t.Run("Body Wrapping", func(t *testing.T) {
		input := []byte(`<html><body>Content</body></html>`)
		output := ToOutlookHTML(input)
		if !bytes.Contains(output, []byte(`<table role="presentation" width="100%"`)) {
			t.Error("Missing body wrapper table")
		}
		if !bytes.Contains(output, []byte(`<td>Content</td>`)) {
			t.Error("Body content not found in wrapper table")
		}
	})

	t.Run("Comment Skipping", func(t *testing.T) {
		input := []byte(`<html><body><!-- <table class="inner"> --></body></html>`)
		output := ToOutlookHTML(input)
		// The <table> inside comment should NOT be normalized
		if bytes.Contains(output, []byte(`class="inner" role="presentation"`)) {
			t.Error("Normalized a table inside a comment")
		}
	})
}

func TestMSOTable(t *testing.T) {
	output := MSOTable("600", "center", "color:red;", "Content")
	if !bytes.Contains([]byte(output), []byte(`width="600"`)) {
		t.Error("Missing width")
	}
	if !bytes.Contains([]byte(output), []byte(`align="center"`)) {
		t.Error("Missing align")
	}
	if !bytes.Contains([]byte(output), []byte(`style="color:red;"`)) {
		t.Error("Missing style")
	}
	if !bytes.Contains([]byte(output), []byte(`role="presentation"`)) {
		t.Error("Missing role")
	}
}

func TestSetOutlookBody(t *testing.T) {
	email := &Email{}
	err := email.SetOutlookBody("<html><body>Hello</body></html>", nil)
	if err != nil {
		t.Fatalf("SetOutlookBody failed: %v", err)
	}
	if !bytes.Contains(email.Body, []byte(`xmlns:v="urn:schemas-microsoft-com:vml"`)) {
		t.Error("Body should be Outlook compatible")
	}
}

func TestOutlookHelpers(t *testing.T) {
	t.Run("WrapInGhostTable", func(t *testing.T) {
		html := "<div>Content</div>"
		wrapped := WrapInGhostTable(html, "600", "center")
		if !bytes.Contains([]byte(wrapped), []byte(`width="600"`)) {
			t.Error("Missing width")
		}
		if !bytes.Contains([]byte(wrapped), []byte(`align="center"`)) {
			t.Error("Missing align")
		}
		if !bytes.Contains([]byte(wrapped), []byte(`<!--[if mso]>`)) {
			t.Error("Missing MSO conditional")
		}
	})

	t.Run("MSOOnly", func(t *testing.T) {
		html := "<div>MSO only</div>"
		wrapped := MSOOnly(html)
		if wrapped != "<!--[if mso]>"+html+"<![endif]-->" {
			t.Errorf("got %s", wrapped)
		}
	})

	t.Run("HideFromMSO", func(t *testing.T) {
		html := "<div>Hide from MSO</div>"
		wrapped := HideFromMSO(html)
		if wrapped != "<!--[if !mso]><!-->"+html+"<!--<![endif]-->" {
			t.Errorf("got %s", wrapped)
		}
	})

	t.Run("MSOButton", func(t *testing.T) {
		cfg := ButtonConfig{
			Text:    "Click Me",
			Link:    "https://example.com",
			BgColor: "#ff0000",
		}
		btn := MSOButton(cfg)
		if !bytes.Contains([]byte(btn), []byte(`fillcolor="#ff0000"`)) {
			t.Error("Missing BgColor in VML")
		}
		if !bytes.Contains([]byte(btn), []byte(`background-color:#ff0000`)) {
			t.Error("Missing BgColor in CSS")
		}
		if !bytes.Contains([]byte(btn), []byte(`Click Me`)) {
			t.Error("Missing Text")
		}
	})

	t.Run("MSOImage", func(t *testing.T) {
		img := MSOImage("img.png", "Alt", 100, 50, "margin:auto")
		if !bytes.Contains([]byte(img), []byte(`width="100"`)) {
			t.Error("Missing width")
		}
		if !bytes.Contains([]byte(img), []byte(`height="50"`)) {
			t.Error("Missing height")
		}
		if !bytes.Contains([]byte(img), []byte(`-ms-interpolation-mode:bicubic`)) {
			t.Error("Missing interpolation mode")
		}
	})

	t.Run("MSOFontStack", func(t *testing.T) {
		stack := MSOFontStack("Arial", "Helvetica", "sans-serif")
		if stack != "Arial, Helvetica, sans-serif" {
			t.Errorf("got %s", stack)
		}
		stackWithSpace := MSOFontStack("Open Sans", "Arial")
		if stackWithSpace != "'Open Sans', Arial" {
			t.Errorf("got %s", stackWithSpace)
		}
	})

	t.Run("MSOSpacer", func(t *testing.T) {
		spacer := MSOSpacer(20)
		if !bytes.Contains([]byte(spacer), []byte(`height="20"`)) {
			t.Error("Missing height in table")
		}
		if !bytes.Contains([]byte(spacer), []byte(`height:20px`)) {
			t.Error("Missing height in div")
		}
	})

	t.Run("MSOBackground", func(t *testing.T) {
		bg := MSOBackground("bg.png", "#ffffff", 600, 400, "Content")
		if !bytes.Contains([]byte(bg), []byte(`src="bg.png"`)) {
			t.Error("Missing image URL in VML")
		}
		if !bytes.Contains([]byte(bg), []byte(`color="#ffffff"`)) {
			t.Error("Missing color in VML")
		}
		if !bytes.Contains([]byte(bg), []byte(`width:600px`)) {
			t.Error("Missing width in VML")
		}
		if !bytes.Contains([]byte(bg), []byte(`Content`)) {
			t.Error("Missing content")
		}
	})

	t.Run("MSOColumns", func(t *testing.T) {
		cols := MSOColumns([]int{300, 300}, "Col 1", "Col 2")
		if !bytes.Contains([]byte(cols), []byte(`width:300px`)) {
			t.Error("Missing column width")
		}
		if !bytes.Contains([]byte(cols), []byte(`Col 1`)) {
			t.Error("Missing column 1 content")
		}
		if !bytes.Contains([]byte(cols), []byte(`Col 2`)) {
			t.Error("Missing column 2 content")
		}
		if !bytes.Contains([]byte(cols), []byte(`<!--[if mso]>`)) {
			t.Error("Missing MSO ghost table")
		}
	})

	t.Run("MSOBulletList", func(t *testing.T) {
		list := MSOBulletList([]string{"Item 1", "Item 2"}, ">", "color:red;")
		if !bytes.Contains([]byte(list), []byte(`Item 1`)) {
			t.Error("Missing item 1")
		}
		if !bytes.Contains([]byte(list), []byte(`>`)) {
			t.Error("Missing custom bullet")
		}
		if !bytes.Contains([]byte(list), []byte(`color:red;`)) {
			t.Error("Missing custom style")
		}
	})
}

func BenchmarkToOutlookHTML(b *testing.B) {
	html := []byte(`<!DOCTYPE html>
<html>
<head>
    <title>Test Email</title>
</head>
<body>
    <h1>Hello, World!</h1>
    <p>This is a test email with some content to benchmark Outlook conversion.</p>
    <table>
        <tr>
            <td>Cell 1</td>
            <td>Cell 2</td>
        </tr>
    </table>
</body>
</html>`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ToOutlookHTML(html)
	}
}

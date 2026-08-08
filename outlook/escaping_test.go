package outlook

import (
	"strings"
	"testing"
)

// The MSO* output becomes the template *source* for SetHTMLBody, so
// html/template's contextual escaping never sees it. Escaping has to happen
// in the helpers themselves.
func TestMSOHelpersEscapeUntrustedInput(t *testing.T) {
	t.Run("button link cannot break out of href", func(t *testing.T) {
		got := MSOButton(ButtonConfig{Text: "Click", Link: `https://x.test/" onmouseover="steal()`})
		if strings.Contains(got, `onmouseover="steal()`) {
			t.Errorf("attribute injection survived:\n%s", got)
		}
	})

	t.Run("button text cannot inject markup", func(t *testing.T) {
		got := MSOButton(ButtonConfig{Text: `<script>alert(1)</script>`, Link: "https://x.test"})
		if strings.Contains(got, "<script>") {
			t.Errorf("raw markup survived:\n%s", got)
		}
		if !strings.Contains(got, "&lt;script&gt;") {
			t.Error("expected the label to be escaped, not dropped")
		}
	})

	t.Run("dangerous schemes are neutralised", func(t *testing.T) {
		// Case games, embedded control characters and entity-encoded prefixes
		// are the usual ways a scheme filter gets bypassed.
		for _, bad := range []string{
			"javascript:alert(1)",
			"JaVaScRiPt:alert(1)",
			"  javascript:alert(1)",
			"java\tscript:alert(1)",
			"java\nscript:alert(1)",
			"jav\rascript:alert(1)",
			"java\x00script:alert(1)",
			"\x01javascript:alert(1)",
			"javascript\t:alert(1)",
			"&#106;avascript:alert(1)",
			"data:text/html;base64,PHNjcmlwdD4=",
			"vbscript:msgbox(1)",
			"file:///etc/passwd",
		} {
			got := safeURL(bad)
			if got != "#" {
				t.Errorf("safeURL(%q) = %q, want an inert #", bad, got)
			}
		}
	})

	t.Run("safe schemes are preserved", func(t *testing.T) {
		for _, ok := range []string{"https://x.test/a?b=c", "http://x.test", "mailto:a@b.test", "tel:+15551234", "cid:logo", "/relative/path"} {
			got := MSOButton(ButtonConfig{Text: "Click", Link: ok})
			if strings.Contains(got, `href="#"`) {
				t.Errorf("%q should have been allowed, got:\n%s", ok, got)
			}
		}
	})

	t.Run("preheader escapes text", func(t *testing.T) {
		got := MSOPreheader(`</div><img src=x onerror=alert(1)>`)
		if strings.Contains(got, "<img") {
			t.Errorf("preheader injection survived: %s", got)
		}
	})

	t.Run("image src and alt are escaped", func(t *testing.T) {
		got := MSOImage(`x" onerror="alert(1)`, `a" onload="b`, 0, 0, "")
		if strings.Contains(got, `onerror="alert(1)`) || strings.Contains(got, `onload="b`) {
			t.Errorf("img injection survived: %s", got)
		}
	})

	t.Run("style attributes reject expression and tag breakouts", func(t *testing.T) {
		if got := MSOImage("https://x.test/i.png", "alt", 0, 0, `x"><script>alert(1)</script>`); strings.Contains(got, "<script>") {
			t.Errorf("style injection survived: %s", got)
		}
		if got := MSOTable("100%", "left", "width:expression(alert(1))", "hi"); strings.Contains(got, "expression(") {
			t.Errorf("CSS expression survived: %s", got)
		}
	})

	t.Run("emoji and bullet list escape text", func(t *testing.T) {
		if got := MSOEmoji("<b>x</b>"); strings.Contains(got, "<b>") {
			t.Errorf("emoji injection survived: %s", got)
		}
		if got := MSOBulletList([]string{"<i>x</i>"}, "", ""); strings.Contains(got, "<i>") {
			t.Errorf("bullet list injection survived: %s", got)
		}
	})

	t.Run("trusted HTML fragments still pass through", func(t *testing.T) {
		// Content parameters are documented as trusted markup; composing
		// fragments is the point of these helpers.
		if got := WrapInGhostTable("<div>ok</div>", "600px", "center"); !strings.Contains(got, "<div>ok</div>") {
			t.Errorf("trusted content was mangled: %s", got)
		}
		if got := MSOTable("100%", "left", "", "<span>ok</span>"); !strings.Contains(got, "<span>ok</span>") {
			t.Errorf("trusted content was mangled: %s", got)
		}
	})
}

package outlook

import (
	"net/url"
	"strings"
	"testing"
)

// These builders produce the template source that SetHTMLBody parses, so
// html/template's contextual escaping never inspects it. Whatever they emit is
// what reaches the recipient.

func FuzzSafeURL(f *testing.F) {
	f.Add("https://example.com/a?b=c")
	f.Add("javascript:alert(1)")
	f.Add("  JaVaScRiPt:alert(1)")
	f.Add("java\tscript:alert(1)")
	f.Add("data:text/html;base64,x")
	f.Add("mailto:a@b.test")
	f.Add("/relative")
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		got := safeURL(raw)

		// Check the scheme, not a substring: "/dAtA:" is a relative path whose
		// text happens to contain "data:", and rejecting it would be wrong.
		if u, err := url.Parse(got); err == nil && u.Scheme != "" {
			if _, ok := safeURLSchemes[strings.ToLower(u.Scheme)]; !ok {
				t.Fatalf("safeURL(%q) = %q, whose scheme %q is not allowed", raw, got, u.Scheme)
			}
		}
		// The result lands inside a double-quoted attribute, so it may not
		// contain the delimiter.
		if strings.ContainsAny(got, `"<>`) {
			t.Fatalf("safeURL(%q) = %q, which would break out of the attribute", raw, got)
		}
	})
}

// A button label and link are the two fields a real application fills from
// user input.
func FuzzMSOButton(f *testing.F) {
	f.Add("Click", "https://example.com")
	f.Add(`<script>alert(1)</script>`, `https://x/" onmouseover="steal()`)
	f.Add("", "")

	f.Fuzz(func(t *testing.T, text, link string) {
		benign := MSOButton(ButtonConfig{Text: "x", Link: "https://x.test"})
		got := MSOButton(ButtonConfig{Text: text, Link: link})
		assertStructureUnchanged(t, benign, got, text, link)
	})
}

func FuzzMSOImage(f *testing.F) {
	f.Add("https://x/i.png", "alt text", "color:red")
	f.Add(`x" onerror="alert(1)`, `a" onload="b`, `x"><script>`)

	f.Fuzz(func(t *testing.T, src, alt, style string) {
		benign := MSOImage("https://x.test/i.png", "alt", 0, 0, "color:red")
		got := MSOImage(src, alt, 0, 0, style)
		assertStructureUnchanged(t, benign, got, src, alt, style)
	})
}

func FuzzMSOPreheader(f *testing.F) {
	f.Add("Preview text")
	f.Add(`</div><img src=x onerror=alert(1)>`)
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		if text == "" {
			return // documented to produce nothing
		}
		assertStructureUnchanged(t, MSOPreheader("x"), MSOPreheader(text), text)
	})
}

// assertStructureUnchanged requires that untrusted input contribute text but
// never structure.
//
// Searching the output for "onerror=" is the wrong test: escaping turns a
// hostile string into visible text that still *contains* those bytes, so a
// substring check reports an injection that is not there. What actually
// matters is whether the input introduced a markup delimiter. Rendering the
// same builder with a benign input and comparing delimiter counts catches a
// real breakout without needing an HTML parser, and cannot be fooled by
// escaped text.
func assertStructureUnchanged(t *testing.T, benign, got string, inputs ...string) {
	t.Helper()

	for _, delim := range []string{`"`, "<", ">"} {
		if want, have := strings.Count(benign, delim), strings.Count(got, delim); want != have {
			t.Fatalf("untrusted input changed the markup structure: %d %q in the benign render, %d in this one\ninputs: %q\noutput: %s",
				want, delim, have, inputs, got)
		}
	}
}

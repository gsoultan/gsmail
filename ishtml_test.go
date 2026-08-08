package gsmail

import "testing"

// IsHTML decides whether a body goes out as text/plain or text/html, and every
// provider consults it. A false positive means the recipient's renderer eats
// the "tag" and the sentence loses a word.
func TestIsHTML(t *testing.T) {
	html := []string{
		"<html><body>hi</body></html>",
		"<!DOCTYPE html><html>",
		"<div>content</div>",
		"<p>paragraph</p>",
		"<p class=\"x\">paragraph</p>",
		"<h1>Heading</h1>",
		"<h6 class='x'>Heading</h6>",
		"<br/><div/>",
		"text before <div>then markup</div>",
		"<DIV>uppercase</DIV>",
		"<Body>mixed case</Body>",
	}
	for _, s := range html {
		if !IsHTML([]byte(s)) {
			t.Errorf("IsHTML(%q) = false, want true", s)
		}
	}

	// Prose that merely contains an angle bracket is not HTML.
	plain := []string{
		"Please review <p1> pricing before Friday.",
		"Temperature dropped <halfway> through the run.",
		"Use the <divider> component.",
		"if (a <p) { return }",
		"Compare <html5 and <html9 builds",
		"Set the <henchman> flag",
		"Plain text, no markup at all.",
		"Meeting at 3pm.",
		"a < b and c > d",
		"",
		"<",
		"<p",
	}
	for _, s := range plain {
		if IsHTML([]byte(s)) {
			t.Errorf("IsHTML(%q) = true, want false", s)
		}
	}
}

// Only the first 1 KiB is scanned, by design.
func TestIsHTMLOnlyScansThePrefix(t *testing.T) {
	pad := make([]byte, 2048)
	for i := range pad {
		pad[i] = 'x'
	}
	if IsHTML(append(pad, []byte("<div>late</div>")...)) {
		t.Error("markup beyond the 1 KiB window should not be detected")
	}
}

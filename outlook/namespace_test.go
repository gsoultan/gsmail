package outlook

import (
	"bytes"
	"strings"
	"testing"
)

// Duplicate attributes are invalid HTML. Parsers keep the first and discard the
// rest, so nothing visibly breaks — which is why this went unnoticed: the
// document merely failed validation and grew a little on every pass.
//
// Any generator that emits VML has to declare xmlns:v and xmlns:o itself, so
// this was the common case rather than an edge one.

func countAttr(html []byte, attr string) int {
	openTag := html
	if i := bytes.IndexByte(html[bytes.Index(html, []byte("<html")):], '>'); i != -1 {
		start := bytes.Index(html, []byte("<html"))
		openTag = html[start : start+i+1]
	}
	return strings.Count(string(openTag), attr)
}

func TestNamespacesAreNotDuplicated(t *testing.T) {
	in := []byte(`<!DOCTYPE html><html lang="en" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office"><head><title>t</title></head><body>hi</body></html>`)

	out := ToOutlookHTML(in)

	for _, attr := range []string{"xmlns:v=", "xmlns:o="} {
		if n := countAttr(out, attr); n != 1 {
			t.Errorf("%s appears %d times on <html>, want 1", attr, n)
		}
	}
	// The ones that were missing still have to be added.
	for _, attr := range []string{"xmlns:w=", "xmlns:m="} {
		if n := countAttr(out, attr); n != 1 {
			t.Errorf("%s appears %d times on <html>, want 1", attr, n)
		}
	}
}

func TestAllNamespacesAreAddedWhenNoneArePresent(t *testing.T) {
	in := []byte(`<!DOCTYPE html><html lang="en"><head></head><body>hi</body></html>`)
	out := ToOutlookHTML(in)

	for _, attr := range []string{"xmlns:v=", "xmlns:o=", "xmlns:w=", "xmlns:m="} {
		if n := countAttr(out, attr); n != 1 {
			t.Errorf("%s appears %d times, want 1", attr, n)
		}
	}
}

func TestNoNamespaceIsAddedTwiceWhenAllArePresent(t *testing.T) {
	in := []byte(`<html xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:w="urn:schemas-microsoft-com:office:word" xmlns:m="http://schemas.microsoft.com/office/2004/12/omml" lang="en"><head></head><body>hi</body></html>`)
	out := ToOutlookHTML(in)

	for _, attr := range []string{"xmlns:v=", "xmlns:o=", "xmlns:w=", "xmlns:m="} {
		if n := countAttr(out, attr); n != 1 {
			t.Errorf("%s appears %d times, want 1", attr, n)
		}
	}
}

// Running the conversion twice must not keep growing the tag. A pipeline that
// hardens on save and again on send would otherwise accumulate attributes.
func TestConversionIsIdempotent(t *testing.T) {
	in := []byte(`<!DOCTYPE html><html lang="en"><head><title>t</title></head><body>hi</body></html>`)

	once := ToOutlookHTML(in)
	twice := ToOutlookHTML(once)

	for _, attr := range []string{"xmlns:v=", "xmlns:o=", "xmlns:w=", "xmlns:m=", "lang="} {
		if n := countAttr(twice, attr); n != 1 {
			t.Errorf("after two passes %s appears %d times, want 1", attr, n)
		}
	}
	if len(twice) != len(once) {
		t.Errorf("a second pass changed the document: %d bytes then %d", len(once), len(twice))
	}
}

// A caller that hardens at more than one point in a pipeline — on save and
// again on send, say — must not ship a document several times larger than it
// needs to be. That matters because Gmail clips at 102KB and silently drops
// everything after the cut, including the unsubscribe footer.
func TestRepeatedConversionsAddNothing(t *testing.T) {
	inputs := map[string][]byte{
		"full document": []byte(`<!DOCTYPE html><html lang="en"><head><title>t</title></head><body>hi</body></html>`),
		"fragment":      []byte(`<p>hello</p>`),
		"no head":       []byte(`<html><body>hi</body></html>`),
	}

	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			once := ToOutlookHTML(in)
			twice := ToOutlookHTML(once)
			thrice := ToOutlookHTML(twice)

			if len(twice) != len(once) || len(thrice) != len(once) {
				t.Errorf("document grew across passes: %d, %d, %d bytes", len(once), len(twice), len(thrice))
			}
			for _, m := range []string{"<meta charset", "[if gte mso 9]", "OutlookHolder", "PixelsPerInch"} {
				a, b := strings.Count(string(once), m), strings.Count(string(thrice), m)
				if a != b {
					t.Errorf("%q appears %d times after one pass and %d after three", m, a, b)
				}
			}
		})
	}
}

func TestAlreadyConvertedReportsHonestly(t *testing.T) {
	raw := []byte(`<html><body>hi</body></html>`)
	if AlreadyConverted(raw) {
		t.Error("a raw document should not be reported as converted")
	}
	if !AlreadyConverted(ToOutlookHTML(raw)) {
		t.Error("a converted document should be reported as converted")
	}
}

// The sentinel is a comment, so it must not be visible to a reader.
func TestTheSentinelIsAComment(t *testing.T) {
	out := string(ToOutlookHTML([]byte(`<p>hello</p>`)))
	if !strings.Contains(out, "<!--gsmail:outlook-->") {
		t.Fatal("the sentinel is missing")
	}
	if strings.Contains(out, "gsmail:outlook<") || strings.Contains(out, ">gsmail:outlook") {
		t.Error("the sentinel escaped its comment")
	}
}

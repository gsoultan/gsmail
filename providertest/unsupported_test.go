package providertest

import "testing"

// A misspelled field name has to be loud. If it were accepted and matched
// nothing, the check it was meant to disable would keep running and the one it
// was meant to name would not be skipped -- and the provider author would read
// a green suite either way. That is the zero-value failure this list replaced.
func TestUnsupportedRejectsUnknownField(t *testing.T) {
	for _, name := range []string{
		"Attachment",   // singular; the constant is Attachments
		"to",           // wrong case
		"Disposition ", // trailing space
		"Headers",      // real Sent field, but SkipHeaderChecks owns it
		"",
	} {
		if _, err := newUnsupported([]string{name}); err == nil {
			t.Errorf("newUnsupported(%q) = nil error, want a rejection", name)
		}
	}
}

func TestUnsupportedAcceptsEveryConstant(t *testing.T) {
	all := []string{
		FieldTo, FieldCc, FieldBcc, FieldSubject,
		FieldText, FieldHTML, FieldAttachments, FieldDisposition,
	}

	u, err := newUnsupported(all)
	if err != nil {
		t.Fatalf("newUnsupported(all constants): %v", err)
	}
	if len(u) != len(knownFields) {
		t.Errorf("declared %d fields but knownFields has %d; a constant is missing from one of them",
			len(u), len(knownFields))
	}
	for _, f := range all {
		if u.expresses(f) {
			t.Errorf("%s was declared unsupported but expresses reports it carried", f)
		}
	}
}

// The nil set is what RunSMTP passes: it decodes off the wire, so every field
// is expressed by construction.
func TestUnsupportedNilExpressesEverything(t *testing.T) {
	var u unsupported
	for f := range knownFields {
		if !u.expresses(f) {
			t.Errorf("the nil set does not express %s", f)
		}
	}
}

func TestUnsupportedExpressesUndeclaredFields(t *testing.T) {
	u, err := newUnsupported([]string{FieldBcc})
	if err != nil {
		t.Fatalf("newUnsupported: %v", err)
	}
	if u.expresses(FieldBcc) {
		t.Error("Bcc was declared unsupported but expresses reports it carried")
	}
	if !u.expresses(FieldCc) {
		t.Error("declaring Bcc unsupported also suppressed the Cc check")
	}
}

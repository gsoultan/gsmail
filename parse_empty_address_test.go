package gsmail

import (
	"errors"
	"strings"
	"testing"
)

// (nil, nil) reads as success. A caller writing the idiomatic
//
//	a, err := ParseEmailAddress(s)
//	if err != nil { return err }
//	use(a.Address)
//
// dereferences nil when s was empty, and nothing in the signature warns them.
func TestParseEmailAddressRejectsTheEmptyString(t *testing.T) {
	a, err := ParseEmailAddress("")
	if !errors.Is(err, ErrEmptyAddress) {
		t.Fatalf("error = %v, want ErrEmptyAddress", err)
	}
	if a != nil {
		t.Fatalf("address = %v, want nil", a)
	}
}

// The contract worth pinning is the general one, not just the empty case: a
// nil address must never come back with a nil error, whatever the input.
func TestParseEmailAddressNeverReturnsNilWithoutAnError(t *testing.T) {
	inputs := []string{
		"",
		" ",
		"\t",
		"\n",
		"not an address",
		"<>",
		"<<<>>>",
		"@",
		"a@",
		"@b.com",
		`"unterminated <a@b.com>`,
		"a@b.com",
		"Name <a@b.com>",
		`"Doe, John" <j@example.com>`,
		strings.Repeat("a", 500) + "@example.com",
	}

	for _, in := range inputs {
		a, err := ParseEmailAddress(in)
		if a == nil && err == nil {
			t.Errorf("ParseEmailAddress(%q) returned (nil, nil)", in)
		}
		if a != nil && err != nil {
			t.Errorf("ParseEmailAddress(%q) returned both an address and an error", in)
		}
	}
}

// The five call sites inside this module all guard with "a != nil" because of
// the old contract. The change must not alter what any of them decide.
func TestEmptyAddressHelpersAreUnchanged(t *testing.T) {
	if got := NormalizeAddress(""); got != "" {
		t.Errorf("NormalizeAddress(\"\") = %q, want empty", got)
	}
	if got := NormalizeAddress("  "); got != "" {
		t.Errorf("NormalizeAddress(\"  \") = %q, want empty", got)
	}
	// The ordinary path is untouched.
	if got := NormalizeAddress("Alice <Alice@Example.COM>"); got != "alice@example.com" {
		t.Errorf("NormalizeAddress() = %q, want alice@example.com", got)
	}
	if got := FormatAddress(""); got != "" {
		t.Errorf("FormatAddress(\"\") = %q, want empty", got)
	}
}

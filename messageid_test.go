package gsmail

import (
	"strings"
	"testing"
)

func TestMessageIDDomain(t *testing.T) {
	tests := []struct{ from, want string }{
		{"a@example.com", "example.com"},
		{"Display Name <a@example.com>", "example.com"},
		{`"Doe, John" <j@sub.example.co.uk>`, "sub.example.co.uk"},
		{"  spaced@example.com  ", "example.com"},
		{"", "gsmail.local"},
		{"no-at-sign", "gsmail.local"},
		{"trailing@", "gsmail.local"},
		// Hostile: anything outside letters/digits/dot/hyphen must be refused.
		{"a@exam ple.com", "gsmail.local"},
		{"a@example.com>\r\nBcc: victim@evil.test", "gsmail.local"},
		{"a@good.test\r\nBcc: x@evil.test", "gsmail.local"},
		{"a@good.test x@evil.test", "gsmail.local"},
		{"Evil <a@evil.test> <b@good.test>", "good.test"},
		{"name@display.test <real@good.test>", "good.test"},
		{"a@exa\x00mple.com", "gsmail.local"},
		{"a@[192.0.2.1]", "gsmail.local"},
	}
	for _, tt := range tests {
		if got := messageIDDomain(tt.from); got != tt.want {
			t.Errorf("messageIDDomain(%q) = %q, want %q", tt.from, got, tt.want)
		}
	}
}

func TestGeneratedMessageIDIsWellFormed(t *testing.T) {
	for _, from := range []string{
		"a@example.com",
		"Name <a@example.com>",
		"a@example.com>\r\nBcc: victim@evil.test",
		"garbage",
	} {
		id := generateMessageID(from)
		if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
			t.Errorf("from %q: malformed Message-ID %q", from, id)
		}
		if strings.ContainsAny(id, "\r\n \t") {
			t.Errorf("from %q: Message-ID contains a separator: %q", from, id)
		}
		if strings.Count(id, "@") != 1 {
			t.Errorf("from %q: Message-ID has %d '@': %q", from, strings.Count(id, "@"), id)
		}
	}
}

// Two renders must not collide.
func TestGeneratedMessageIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 2000)
	for i := 0; i < 2000; i++ {
		id := generateMessageID("a@example.com")
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate Message-ID after %d iterations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

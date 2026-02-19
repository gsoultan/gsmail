package gsmail

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestDomainHealth(t *testing.T) {
	// Backup original lookup functions
	oldLookupMX := lookupMX
	oldLookupTXT := lookupTXT
	defer func() {
		lookupMX = oldLookupMX
		lookupTXT = oldLookupTXT
	}()

	t.Run("ValidAll", func(t *testing.T) {
		lookupMX = func(ctx context.Context, domain string) ([]*net.MX, error) {
			if domain == "example.com" {
				return []*net.MX{{Host: "mail.example.com", Pref: 10}}, nil
			}
			return nil, nil
		}
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			switch name {
			case "example.com":
				return []string{"v=spf1 include:_spf.google.com ~all"}, nil
			case "_dmarc.example.com":
				return []string{"v=DMARC1; p=quarantine"}, nil
			case "default._domainkey.example.com":
				return []string{"v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQD"}, nil
			}
			return nil, &net.DNSError{IsNotFound: true}
		}

		health, err := CheckDomainHealth(t.Context(), "example.com", []string{"default"})
		if err != nil {
			t.Fatalf("CheckDomainHealth failed: %v", err)
		}

		if !health.MX.Valid || !health.MX.Found {
			t.Errorf("Expected valid MX, got %+v", health.MX)
		}
		if !health.SPF.Valid || !health.SPF.Found {
			t.Errorf("Expected valid SPF, got %+v", health.SPF)
		}
		if !health.DMARC.Valid || !health.DMARC.Found {
			t.Errorf("Expected valid DMARC, got %+v", health.DMARC)
		}
		if !health.DKIM["default"].Valid || !health.DKIM["default"].Found {
			t.Errorf("Expected valid DKIM, got %+v", health.DKIM["default"])
		}
	})

	t.Run("MissingRecords", func(t *testing.T) {
		lookupMX = func(ctx context.Context, domain string) ([]*net.MX, error) {
			return nil, nil
		}
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			return nil, &net.DNSError{IsNotFound: true}
		}

		health, err := CheckDomainHealth(t.Context(), "nonexistent.com", []string{"default"})
		if err != nil {
			t.Fatalf("CheckDomainHealth failed: %v", err)
		}

		if health.MX.Found {
			t.Errorf("Expected MX not found, got %+v", health.MX)
		}
		if health.SPF.Found {
			t.Errorf("Expected SPF not found, got %+v", health.SPF)
		}
		if health.DMARC.Found {
			t.Errorf("Expected DMARC not found, got %+v", health.DMARC)
		}
		if health.DKIM["default"].Found {
			t.Errorf("Expected DKIM not found, got %+v", health.DKIM["default"])
		}
	})

	t.Run("InvalidMultipleSPF", func(t *testing.T) {
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			if name == "example.com" {
				return []string{"v=spf1 a ~all", "v=spf1 include:other.com ~all"}, nil
			}
			return nil, &net.DNSError{IsNotFound: true}
		}

		spf := CheckSPF(t.Context(), "example.com")
		if spf.Valid {
			t.Errorf("Expected invalid SPF due to multiples, got valid")
		}
		if spf.Details != "Multiple SPF records found (invalid configuration)" {
			t.Errorf("Unexpected details: %s", spf.Details)
		}
	})

	t.Run("InvalidDKIMMissingP", func(t *testing.T) {
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			if name == "default._domainkey.example.com" {
				return []string{"v=DKIM1; k=rsa;"}, nil // missing p=
			}
			return nil, &net.DNSError{IsNotFound: true}
		}

		dkim := CheckDKIM(t.Context(), "example.com", "default")
		if dkim.Valid {
			t.Errorf("Expected invalid DKIM due to missing p=, got valid")
		}
	})

	t.Run("InvalidDKIMRevoked", func(t *testing.T) {
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			if name == "default._domainkey.example.com" {
				return []string{"v=DKIM1; k=rsa; p="}, nil // empty p=
			}
			return nil, &net.DNSError{IsNotFound: true}
		}

		dkim := CheckDKIM(t.Context(), "example.com", "default")
		if dkim.Valid {
			t.Errorf("Expected invalid DKIM due to revoked key (p=), got valid")
		}
		if dkim.Details != "DKIM public key has been revoked (p= is empty)" {
			t.Errorf("Unexpected details: %s", dkim.Details)
		}
	})

	t.Run("InvalidMultipleDKIM", func(t *testing.T) {
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			if name == "default._domainkey.example.com" {
				return []string{"v=DKIM1; p=key1", "v=DKIM1; p=key2"}, nil
			}
			return nil, &net.DNSError{IsNotFound: true}
		}

		dkim := CheckDKIM(t.Context(), "example.com", "default")
		if dkim.Valid {
			t.Errorf("Expected invalid DKIM due to multiples, got valid")
		}
		if !strings.Contains(dkim.Details, "Multiple DKIM records found") {
			t.Errorf("Unexpected details: %s", dkim.Details)
		}
	})

	t.Run("ContextCanceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := CheckDomainHealth(ctx, "example.com", []string{"default"})
		if err == nil {
			t.Errorf("Expected error from canceled context, got nil")
		}
	})

	t.Run("DNSErrorTemporary", func(t *testing.T) {
		lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			return nil, &net.DNSError{IsTemporary: true, Err: "timeout"}
		}

		spf := CheckSPF(t.Context(), "example.com")
		if spf.Error == "" {
			t.Errorf("Expected error message for temporary failure, got empty (Found: %v)", spf.Found)
		}
	})
}

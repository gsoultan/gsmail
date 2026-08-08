package gsmail

import (
	"bytes"
	"testing"
)

// These exercise the Email integration points that remain in this package
// after the Outlook builders moved to gsmail/outlook.

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

func TestSetOutlookBody(t *testing.T) {
	email := &Email{}
	err := email.SetOutlookBody("<html><body>Hello</body></html>", nil)
	if err != nil {
		t.Fatalf("SetOutlookBody failed: %v", err)
	}
	// The template is HTML, so SetBody routes it to HTMLBody.
	if !bytes.Contains(email.HTMLBody, []byte(`xmlns:v="urn:schemas-microsoft-com:vml"`)) {
		t.Errorf("HTMLBody should be Outlook compatible, got %q", string(email.HTMLBody))
	}
	if !email.IsOutlookCompatible() {
		t.Error("IsOutlookCompatible should report true")
	}
}

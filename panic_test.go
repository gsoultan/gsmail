package gsmail_test

import (
	"context"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestPanicResistance(t *testing.T) {
	t.Run("SendNilSender", func(t *testing.T) {
		err := gsmail.Send(context.Background(), nil, gsmail.Email{})
		if err == nil {
			t.Error("expected error for nil sender, got nil")
		}
	})

	t.Run("PingNilProvider", func(t *testing.T) {
		err := gsmail.Ping(context.Background(), nil)
		if err == nil {
			t.Error("expected error for nil provider, got nil")
		}
	})
}

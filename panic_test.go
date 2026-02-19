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

	t.Run("ReceiveNilReceiver", func(t *testing.T) {
		_, err := gsmail.Receive(context.Background(), nil, 10)
		if err == nil {
			t.Error("expected error for nil receiver, got nil")
		}
	})

	t.Run("PingNilProvider", func(t *testing.T) {
		err := gsmail.Ping(context.Background(), nil)
		if err == nil {
			t.Error("expected error for nil provider, got nil")
		}
	})
}

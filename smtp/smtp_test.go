package smtp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/textproto"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestSenderAddressParsing(t *testing.T) {
	// Mock SMTP server to record MAIL FROM and RCPT TO
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	host, port, _ := net.SplitHostPort(addr)
	var portInt int
	fmt.Sscanf(port, "%d", &portInt)

	var receivedMailFrom string
	var receivedRcptTo []string

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := textproto.NewReader(bufio.NewReader(conn))
		writer := textproto.NewWriter(bufio.NewWriter(conn))

		writer.PrintfLine("220 localhost ESMTP")
		for {
			line, err := reader.ReadLine()
			if err != nil {
				return
			}
			switch {
			case len(line) >= 4 && line[:4] == "EHLO":
				writer.PrintfLine("250-localhost")
				writer.PrintfLine("250 OK")
			case len(line) >= 10 && line[:10] == "MAIL FROM:":
				receivedMailFrom = line[10:]
				writer.PrintfLine("250 OK")
			case len(line) >= 8 && line[:8] == "RCPT TO:":
				receivedRcptTo = append(receivedRcptTo, line[8:])
				writer.PrintfLine("250 OK")
			case line == "DATA":
				writer.PrintfLine("354 Start mail input; end with <CRLF>.<CRLF>")
				for {
					l, _ := reader.ReadLine()
					if l == "." {
						break
					}
				}
				writer.PrintfLine("250 OK")
			case line == "QUIT":
				writer.PrintfLine("221 Goodbye")
				return
			default:
				writer.PrintfLine("250 OK")
			}
		}
	}()

	sender := NewSender(host, portInt, "", "", false)
	email := gsmail.Email{
		From:    "PT. Impack Pratama <no-reply@impack-pratama.com>",
		To:      []string{"Dimas Prananda <dimas.prananda@impack-pratama.com>"},
		Subject: "Test",
		Body:    []byte("test"),
	}

	err = sender.Send(context.Background(), email)
	if err != nil {
		t.Fatalf("Failed to send: %v", err)
	}

	expectedMailFrom := "<no-reply@impack-pratama.com>"
	if receivedMailFrom != expectedMailFrom {
		t.Errorf("Expected MAIL FROM %s, got %s", expectedMailFrom, receivedMailFrom)
	}

	expectedRcptTo := "<dimas.prananda@impack-pratama.com>"
	if len(receivedRcptTo) != 1 || receivedRcptTo[0] != expectedRcptTo {
		t.Errorf("Expected RCPT TO %s, got %v", expectedRcptTo, receivedRcptTo)
	}
}

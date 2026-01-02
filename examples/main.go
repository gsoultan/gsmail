package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/imap"
	"github.com/gsoultan/gsmail/smtp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Basic Email Structure
	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver1@example.com", "receiver2@example.com"},
		Subject: "Welcome to gsmail",
	}

	// 2. Dynamic Template with Automatic HTML Detection
	data := map[string]interface{}{
		"Name": "Awesome Developer",
		"Date": time.Now().Format("2006-01-02"),
	}

	// Because the string contains <h1> and <div>, gsmail will automatically
	// use the HTML template engine and set the Content-Type to text/html.
	tmpl := `
		<div style="font-family: Arial, sans-serif;">
			<h1>Hello {{.Name}}!</h1>
			<p>Welcome to our platform. Today is {{.Date}}.</p>
			<p>This email was sent using <b>gsmail</b>, a high-performance Go library.</p>
		</div>
	`
	if err := email.SetBody(tmpl, data); err != nil {
		log.Fatalf("Failed to set body: %v", err)
	}

	// 3. Email Validation (Format + Existence)
	target := "receiver1@example.com"
	if gsmail.IsValidEmail(target) {
		fmt.Printf("%s is a valid email format.\n", target)

		// Example of existence check (commented out as it requires network)
		// _ = gsmail.ValidateEmailExistence(ctx, target)
	}

	// Just to use ctx in the example so it compiles without warnings
	_ = ctx

	// 4. Sending via SMTP (Example Config)
	// NewSender(host, port, user, pass, isSSL)
	smtpSender := smtp.NewSender("smtp.example.com", 587, "myuser", "mypass", false)

	// In a real scenario, you would call:
	// err := gsmail.Send(ctx, smtpSender, email)
	fmt.Printf("Ready to send email from %s to %v\n", email.From, email.To)
	_ = smtpSender

	// 5. Receiving via IMAP
	// NewReceiver(host, port, user, pass, isSSL)
	imapReceiver := imap.NewReceiver("imap.example.com", 993, "myuser", "mypass", true)

	fmt.Println("Ready to receive emails via IMAP...")
	// emails, err := imapReceiver.Receive(ctx, 5) // Receive last 5 emails
	_ = imapReceiver

	fmt.Println("Example finished successfully.")
}

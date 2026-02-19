package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/imap"
	"github.com/gsoultan/gsmail/mailgun"
	"github.com/gsoultan/gsmail/otelgs"
	"github.com/gsoultan/gsmail/postmark"
	"github.com/gsoultan/gsmail/sendgrid"
	"github.com/gsoultan/gsmail/smtp"
)

func useProviders() {
	_ = sendgrid.NewSender("SG.api_key")
	_ = mailgun.NewSender("example.com", "mg_api_key")
	_ = postmark.NewSender("pm_server_token")
}

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
	data := map[string]any{
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

	// 4. Domain Health Check (New Feature)
	domain := "example.com"
	fmt.Printf("\nChecking Domain Health for %s...\n", domain)
	// Example call (commented out as it requires network)
	// health, _ := gsmail.CheckDomainHealth(ctx, domain, []string{"google", "mandrill"})
	// fmt.Printf("SPF Valid: %v, DMARC Valid: %v\n", health.SPF.Valid, health.DMARC.Valid)

	// 5. DKIM Signing (New Feature)
	// You can sign any raw email bytes or configure a sender to do it automatically.
	dkimOpts := gsmail.DKIMOptions{
		Domain:   "example.com",
		Selector: "default",
		// PrivateKey can be a PEM string
		PrivateKey: `-----BEGIN RSA PRIVATE KEY-----
... your private key ...
-----END RSA PRIVATE KEY-----`,
	}
	_ = dkimOpts

	// 6. Sending via SMTP (Example Config)
	// NewSender(host, port, user, pass, isSSL)
	smtpSender := smtp.NewSender("smtp.example.com", 587, "user@example.com", "mypass", false)

	// 7. Middleware & Observability (New Feature)
	// Wrap the sender with logging and OpenTelemetry tracing
	wrappedSender := gsmail.WrapSender(smtpSender,
		gsmail.LoggerInterceptor(log.Printf),
		gsmail.RecoveryInterceptor(),
		otelgs.SendInterceptor(),
	)

	// 8. Background Sending (New Feature)
	// For high-throughput apps, send emails in the background
	bgSender := gsmail.NewBackgroundSender(wrappedSender, 100)
	bgSender.Start(5) // Start 5 workers
	defer bgSender.Stop()

	// Add email to background queue
	if bgSender.Send(email) {
		fmt.Println("Email queued for background sending")
	}

	// 9. Receiving via IMAP with Search & IDLE (New Feature)
	imapReceiver := imap.NewReceiver("imap.example.com", 993, "user@example.com", "mypass", true)
	_ = imapReceiver

	// Search for unseen emails from a specific sender
	opts := gsmail.SearchOptions{
		From:   "boss@example.com",
		Unseen: true,
	}
	_ = opts
	fmt.Println("Searching for urgent emails...")
	// emails, _ := imapReceiver.Search(ctx, opts, 10)

	// Use IDLE for real-time notifications
	// emailsChan, errChan := imapReceiver.Idle(ctx)
	// go func() {
	//     for e := range emailsChan {
	//         fmt.Printf("New email received: %s\n", e.Subject)
	//     }
	// }()

	// 10. Bounce & Complaint Handling
	// Example: Parsing a raw DSN email
	rawDSN := []byte("...") // raw bytes from a bounce email
	if dsnEmail, err := gsmail.ParseRawEmail(rawDSN); err == nil {
		if bounce, err := gsmail.ParseBounce(dsnEmail); err == nil {
			fmt.Printf("Detected %s bounce for %s: %s\n", bounce.Type, bounce.EmailAddress, bounce.Reason)
		}
	}

	// Example: Handling AWS SES Webhook
	sesPayload := []byte(`{"notificationType": "Bounce", ...}`)
	if res, err := gsmail.ParseSESWebhook(sesPayload); err == nil {
		if b, ok := res.(*gsmail.Bounce); ok {
			fmt.Printf("SES Bounce: %s\n", b.EmailAddress)
		}
	}

	fmt.Println("Example finished successfully.")
}

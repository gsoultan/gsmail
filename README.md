# gsmail

`gsmail` is a high-performance, zero-allocation oriented Golang email library. It provides a clean, unified interface for sending and receiving emails, with built-in support for dynamic templates and automatic HTML detection.

## Features

- **Pluggable Senders**: Send emails via standard SMTP (with SSL/TLS) or AWS SES.
- **Pluggable Receivers**: Receive emails via POP3 and IMAP.
- **Dynamic Templates**: Built-in support for `text/template` and `html/template`.
- **Flexible Template Loading**: Load templates from strings, HTTP URLs, or AWS S3 compatible storage.
- **Automatic Content Type Detection**: Automatically detects if an email is HTML or Plaintext based on the content.
- **Email Validation**: Includes high-performance `IsValidEmail` (regex) and `ValidateEmailExistence` (MX/SMTP check) utilities.
- **Attachments Support**: Send and receive multiple attachments with automatic MIME encoding/decoding.
- **Zero-Allocation Focus**: Optimized hot paths and `sync.Pool` utilization to minimize heap allocations and pressure on the GC.
- **Modern AWS SDK**: Uses AWS SDK for Go v2.
- **Context Awareness**: Full support for `context.Context` for timeouts and cancellation.
- **Examples Included**: See the `examples/` directory for complete usage scenarios.

## Installation

```bash
go get github.com/gsoultan/gsmail
```

## Quick Start

### Basic SMTP Usage

```go
import (
    "context"
    "github.com/gsoultan/gsmail"
    "github.com/gsoultan/gsmail/smtp"
)

func main() {
    // 1. Create an email
    email := gsmail.Email{
        From:    "sender@example.com",
        To:      []string{"receiver@example.com"},
        Subject: "Hello from gsmail",
    }

    // 2. Set body with template (automatically detects HTML)
    data := map[string]string{"Name": "Developer"}
    email.SetBody("<h1>Hello {{.Name}}!</h1>", data)

    // 3. Choose a sender and send
    sender := smtp.NewSender("smtp.example.com", 587, "user", "pass", false)
    err := gsmail.Send(context.Background(), sender, email)
    if err != nil {
        panic(err)
    }
}
```

### AWS SES Usage

```go
import "github.com/gsoultan/gsmail/ses"

// Choose AWS SES sender
sender := ses.NewSender("us-east-1", "ACCESS_KEY", "SECRET_KEY", "")
err := gsmail.Send(context.Background(), sender, email)
```

### Sending with Attachments

```go
email := gsmail.Email{
    From:    "sender@example.com",
    To:      []string{"receiver@example.com"},
    Subject: "Report with Attachments",
    Body:    []byte("Please find the attached reports."),
    Attachments: []gsmail.Attachment{
        {
            Filename:    "report.pdf",
            ContentType: "application/pdf",
            Data:        pdfBytes,
        },
    },
}
```

## Advanced Features

### Loading Templates from URL

```go
ctx := context.Background()
err := email.SetBodyFromURL(ctx, "https://example.com/templates/welcome.html", data)
```

### Loading Templates from S3

```go
s3Cfg := gsmail.S3Config{
    Region:    "us-east-1",
    Bucket:    "my-templates",
    Key:       "monthly-report.tmpl",
    AccessKey: "S3_ACCESS_KEY",
    SecretKey: "S3_SECRET_KEY",
}
err := email.SetBodyFromS3(ctx, s3Cfg, data)
```

### Receiving Emails (IMAP)

```go
import (
    "fmt"
    "github.com/gsoultan/gsmail/imap"
)

receiver := imap.NewReceiver("imap.example.com", 993, "user", "pass", true)
emails, err := receiver.Receive(context.Background(), 10)
if err != nil {
    panic(err)
}
for _, email := range emails {
    fmt.Printf("From: %s, Subject: %s\n", email.From, email.Subject)
}
```

### Receiving Emails (POP3)

```go
import "github.com/gsoultan/gsmail/pop3"

receiver := pop3.NewReceiver("pop.example.com", 995, "user", "pass", true)
emails, err := receiver.Receive(context.Background(), 10)
// ...
```

### Email Validation

```go
// Basic regex check
isValid := gsmail.IsValidEmail("test@example.com")

// Existence check (MX lookup + SMTP handshake)
err := gsmail.ValidateEmailExistence(ctx, "test@example.com")
if err != nil {
    fmt.Printf("Email does not exist: %v\n", err)
}

// Or use the Validate method on a Sender or Receiver
err = sender.Validate(ctx, "test@example.com")
```

## Performance Guidelines

This library is designed for performance. To get the most out of it, use the recommended build flags:

```bash
go build -ldflags="-s -w" -gcflags="-m -l" .
```

- `-s -w`: Reduces binary size by stripping debug symbols.
- `-gcflags="-m -l"`: `-m` prints optimization decisions (like escape analysis) to help achieve zero allocations, and `-l` disables inlining if needed.

## License

MIT

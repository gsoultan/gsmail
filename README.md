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
- **Outlook Compatibility**: Convert HTML templates to be compatible with Microsoft Outlook with a single flag.
- **Zero-Allocation Focus**: Optimized hot paths and `sync.Pool` utilization to minimize heap allocations and pressure on the GC.
- **Modern AWS SDK**: Uses AWS SDK for Go v2.
- **Context Awareness**: Full support for `context.Context` for timeouts and cancellation.
- **Connection Health Check**: Easily verify connectivity to providers using the `Ping` method.
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
import (
    "context"
    "github.com/gsoultan/gsmail"
    "github.com/gsoultan/gsmail/ses"
)

func main() {
    // ... email creation from previous example

    // Choose AWS SES sender
    sender := ses.NewSender("us-east-1", "ACCESS_KEY", "SECRET_KEY", "")
    err := gsmail.Send(context.Background(), sender, email)
    if err != nil {
        panic(err)
    }
}
```

### Sending with Attachments

```go
// ... assuming email, pdfBytes are defined
email.Attachments = []gsmail.Attachment{
    {
        Filename:    "report.pdf",
        ContentType: "application/pdf",
        Data:        pdfBytes,
    },
}
```

## Advanced Features

### Loading Templates from URL

```go
// ... assuming email, data are defined
err := email.SetBodyFromURL(context.Background(), "https://example.com/templates/welcome.html", data)
```

### Loading Templates from S3

```go
// ... assuming email, data are defined
s3Cfg := gsmail.S3Config{
    Region:    "us-east-1",
    Bucket:    "my-templates",
    Key:       "monthly-report.tmpl",
    AccessKey: "S3_ACCESS_KEY",
    SecretKey: "S3_SECRET_KEY",
}
err := email.SetBodyFromS3(context.Background(), s3Cfg, data)
```

### Receiving Emails (IMAP)

```go
import (
    "context"
    "fmt"
    "github.com/gsoultan/gsmail/imap"
)

func main() {
    receiver := imap.NewReceiver("imap.example.com", 993, "user", "pass", true)
    emails, err := receiver.Receive(context.Background(), 10)
    if err != nil {
        panic(err)
    }
    for _, email := range emails {
        fmt.Printf("From: %s, Subject: %s\n", email.From, email.Subject)
    }
}
```

### Receiving Emails (POP3)

```go
import (
    "context"
    "github.com/gsoultan/gsmail/pop3"
)

func main() {
    receiver := pop3.NewReceiver("pop.example.com", 995, "user", "pass", true)
    emails, err := receiver.Receive(context.Background(), 10)
    if err != nil {
        panic(err)
    }
    // ... process emails
}
```

### Email Validation

```go
// Basic regex check
isValid := gsmail.IsValidEmail("test@example.com")

// Existence check (MX lookup + SMTP handshake)
err := gsmail.ValidateEmailExistence(context.Background(), "test@example.com")
if err != nil {
    fmt.Printf("Email does not exist: %v\n", err)
}

// Or use the Validate method on a Sender or Receiver
err = sender.Validate(context.Background(), "test@example.com")
```

### Outlook Compatibility

Outlook uses Word for rendering, which has limited HTML/CSS support. You can enable Outlook compatibility mode to automatically inject necessary fixes (namespaces, MSO settings, meta tags, and CSS resets):

```go
email := gsmail.Email{
    OutlookCompatible: true,
}
// All subsequent SetBody calls will now apply Outlook fixes
email.SetBody("<html>...</html>", data)

// Or use the shortcut method which sets the flag for you
email.SetOutlookBody("<html>...</html>", data)
```

### Outlook Compatibility Helpers

In addition to the automatic flag, `gsmail` provides helper functions to handle common Outlook layout issues:

- `WrapInGhostTable(html, width, align)`: Wraps content in a conditional MSO table to enforce widths.
- `MSOOnly(html)`: Content visible only in Outlook.
- `HideFromMSO(html)`: Content hidden from Outlook.
- `MSOButton(cfg)`: Generates a "bulletproof" VML button.
- `MSOTable(width, align, style, content)`: Generates a normalized table with standard Outlook fixes.
- `MSOImage(src, alt, width, height, style)`: Generates an image with Outlook fixes.
- `MSOFontStack(fonts...)`: Returns a font stack with proper quoting for Outlook.
- `MSOBackground(url, color, w, h, content)`: Generates a VML-based background for Outlook.
- `MSOColumns(widths, cols...)`: Responsive side-by-side columns using ghost tables.
- `MSOBulletList(items, bullet, style)`: Consistent bulleted lists (avoids Outlook <ul> issues).
- `IsOutlookCompatible(html)`: Detects if HTML contains Outlook-specific fixes.

`gsmail` also automatically injects Dark Mode support markers and CSS when Outlook compatibility is enabled, ensuring your emails look great in both light and dark themes.

```go
data := map[string]interface{}{
    "Content": gsmail.WrapInGhostTable("<div>My Content</div>", "600", "center"),
    "Button":  gsmail.MSOButton(gsmail.ButtonConfig{
        Text: "Click Here",
        Link: "https://example.com",
        BgColor: "#007bff",
    }),
    "Image": gsmail.MSOImage("logo.png", "Logo", 200, 50, ""),
    "Background": gsmail.MSOBackground("bg.png", "#f8f9fa", 600, 400, "<h1>Centered Text</h1>"),
    "Columns": gsmail.MSOColumns([]int{300, 300}, "Left Content", "Right Content"),
    "List": gsmail.MSOBulletList([]string{"Feature A", "Feature B"}, "✔", "color:green;"),
}
```

### Connection Checking (Ping)

Verify that your provider is correctly configured and reachable.

```go
// Ping any Sender or Receiver
err := gsmail.Ping(context.Background(), sender)
if err != nil {
    fmt.Printf("Connection failed: %v\n", err)
}

// Or call Ping directly on the provider
err = receiver.Ping(context.Background())
```

## Performance Guidelines

This library is designed for performance. To get the most out of it, use the recommended build flags:

```bash
go build -ldflags="-s -w" -gcflags="-m -l" .
```

- `-s -w`: Reduces binary size by stripping debug symbols.
- `-gcflags="-m -l"`: `-m` prints optimization decisions (like escape analysis) to help achieve zero allocations, and `-l` disables inlining if needed.
+
+## Benchmarks
+
+`gsmail` is optimized for high performance and low memory allocations. Below are the benchmark results for core utilities:
+
+```text
+BenchmarkIsHTML-12                      160427133                7.527 ns/op          0 B/op           0 allocs/op
+BenchmarkHasHeader-12                   35312084                36.48 ns/op           0 B/op           0 allocs/op
+BenchmarkParseRawEmail-12                 344103              4371 ns/op           5400 B/op          15 allocs/op
+BenchmarkParseRawEmailMultipart-12         60823             20156 ns/op          21045 B/op          75 allocs/op
+```
+
+*Tested on: AMD Ryzen 5 5500U, Go 1.21+*
+
 ## License

MIT

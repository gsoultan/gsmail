package gsmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var (
	emailRegex = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)
	dialer     = &net.Dialer{
		Timeout: 5 * time.Second,
	}
	smtpPort  = "25"
	lookupMX  = net.DefaultResolver.LookupMX
	lookupTXT = net.DefaultResolver.LookupTXT
)

const (
	maxBufferSize = 4096
)

var (
	htmlTags = [][]byte{
		[]byte("<html"),
		[]byte("<body"),
		[]byte("<div"),
		[]byte("<p"),
		[]byte("<!doctype"),
		[]byte("<h"),
	}

	bufferPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 1024)
			return &b
		},
	}
)

// GetBuffer retrieves a byte slice from the pool.
func GetBuffer() *[]byte {
	return bufferPool.Get().(*[]byte)
}

// PutBuffer returns a byte slice to the pool if it's within the size limit.
func PutBuffer(b *[]byte) {
	if b == nil {
		return
	}
	if cap(*b) <= maxBufferSize {
		*b = (*b)[:0]
		bufferPool.Put(b)
	}
}

// NewBufferWriter creates a new BufferWriter for the given buffer.
func NewBufferWriter(b *[]byte) *BufferWriter {
	return &BufferWriter{bufPtr: b}
}

// BufferWriter implements io.Writer for the pooled byte slices.
type BufferWriter struct {
	bufPtr *[]byte
}

// Write appends data to the underlying buffer.
func (w *BufferWriter) Write(p []byte) (n int, err error) {
	*w.bufPtr = append(*w.bufPtr, p...)
	return len(p), nil
}

// HasHeader checks if the given byte slice contains the specified header.
// It searches only within the header section (before the first double newline).
func HasHeader(b []byte, header string) bool {
	if header == "" {
		return false
	}

	// Find the end of the header section
	headerEnd := bytes.Index(b, []byte("\r\n\r\n"))
	if headerEnd == -1 {
		headerEnd = bytes.Index(b, []byte("\n\n"))
	}
	if headerEnd == -1 {
		headerEnd = len(b)
	}

	headerBytes := UnsafeStringToBytes(header)

	// Check the first line
	if isHeaderAt(b, 0, headerBytes) {
		return true
	}

	// Check subsequent lines
	for i := 0; i < headerEnd-len(headerBytes); i++ {
		if b[i] == '\n' && isHeaderAt(b, i+1, headerBytes) {
			return true
		}
	}

	return false
}

func isHeaderAt(b []byte, pos int, header []byte) bool {
	if pos+len(header) >= len(b) {
		return false
	}
	if !matchAt(b, pos, header) {
		return false
	}
	// Header must be followed by a colon
	return b[pos+len(header)] == ':'
}

// IsHTML checks if the given byte slice contains common HTML tags.
// The check is case-insensitive.
func IsHTML(b []byte) bool {
	if len(b) == 0 {
		return false
	}

	// For performance, we only check the first 1024 bytes for tags
	searchLen := len(b)
	if searchLen > 1024 {
		searchLen = 1024
	}
	prefix := b[:searchLen]

	for _, tag := range htmlTags {
		if containsCaseInsensitive(prefix, tag) {
			return true
		}
	}
	return false
}

func containsCaseInsensitive(b []byte, substr []byte) bool {
	if len(substr) == 0 {
		return true
	}
	if len(b) < len(substr) {
		return false
	}
	for i := 0; i <= len(b)-len(substr); i++ {
		if matchAt(b, i, substr) {
			return true
		}
	}
	return false
}

// matchAt checks if b contains substr at pos, ignoring case.
func matchAt(b []byte, pos int, substr []byte) bool {
	if pos < 0 || pos+len(substr) > len(b) {
		return false
	}
	for j := 0; j < len(substr); j++ {
		c1 := b[pos+j]
		c2 := substr[j]
		if c1 != c2 {
			// Fast lowercase conversion for ASCII
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 32
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 32
			}
			if c1 != c2 {
				return false
			}
		}
	}
	return true
}

// UnsafeStringToBytes converts a string to a byte slice without allocation.
// The caller must not modify the returned byte slice.
func UnsafeStringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// UnsafeBytesToString converts a byte slice to a string without allocation.
func UnsafeBytesToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// Disposable domains set for spam prevention.
var disposableDomainsSet = map[string]struct{}{
	"10minutemail.com":   {},
	"tempmail.org":       {},
	"guerrillamail.com":  {},
	"mailinator.com":     {},
	"yopmail.com":        {},
	"sharklasers.com":    {},
	"getnada.com":        {},
	"fakeinbox.com":      {},
	"dispostable.com":    {},
	"maildrop.cc":        {},
	"throwawaymail.com":  {},
	"tempmail.lol":       {},
	"guerrillamail.info": {},
	"emailondeck.com":    {},
	"armyspy.com":        {},
	"cuvox.de":           {},
	"dayrep.com":         {},
	"einrot.com":         {},
	"fleckens.hu":        {},
	"gustr.com":          {},
	"hst.tk":             {},
	"jemoch.com":         {},
	"mailinater.com":     {},
	"moakt.com":          {},
	"rhyta.com":          {},
	"superrito.com":      {},
	"teleworm.us":        {},
}

func isDisposableDomain(domain string) bool {
	d := strings.ToLower(domain)
	_, exists := disposableDomainsSet[d]
	return exists
}

func IsDisposableEmail(email string) bool {
	i := strings.LastIndexByte(email, '@')
	if i < 1 || i >= len(email)-1 {
		return false
	}
	return isDisposableDomain(email[i+1:])
}

// IsValidEmail checks if the given string is a valid email address.
// It uses a fast regex check and common sense length limits.
func IsValidEmail(email string) bool {
	if len(email) < 3 || len(email) > 254 {
		return false
	}
	return emailRegex.MatchString(strings.ToLower(email))
}

// ValidateEmailExistence checks if the email address actually exists.
// It performs an MX lookup and attempts an SMTP handshake.
func ValidateEmailExistence(ctx context.Context, email string) error {
	if !IsValidEmail(email) {
		return fmt.Errorf("invalid email format")
	}

	if IsDisposableEmail(email) {
		return fmt.Errorf("disposable/temporary email address not allowed")
	}

	parts := strings.Split(email, "@")
	domain := parts[1]

	mxs, err := lookupMX(ctx, domain)
	if err != nil {
		return fmt.Errorf("lookup mx: %w", err)
	}
	if len(mxs) == 0 {
		return fmt.Errorf("no mx records found for domain %s", domain)
	}

	var lastErr error
	for _, mx := range mxs {
		addr := net.JoinHostPort(mx.Host, smtpPort)
		if err := verifyExistence(ctx, addr, email); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("could not verify email existence: %w", lastErr)
}

func verifyExistence(ctx context.Context, addr, email string) error {
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()

	commands := []func() error{
		func() error { return client.Hello("localhost") },
		func() error { return client.Mail("verify@example.com") },
		func() error { return client.Rcpt(email) },
	}

	for _, cmd := range commands {
		if err := cmd(); err != nil {
			return err
		}
	}

	_ = client.Quit()
	return nil
}

func ParseRawEmail(raw []byte) (Email, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Email{}, fmt.Errorf("read message: %w", err)
	}

	dec := new(mime.WordDecoder)
	subject, _ := dec.DecodeHeader(msg.Header.Get("Subject"))

	email := Email{
		From:    msg.Header.Get("From"),
		Subject: subject,
		ReplyTo: msg.Header.Get("Reply-To"),
	}

	if to := msg.Header.Get("To"); to != "" {
		email.To = parseAddressList(to)
	}

	if cc := msg.Header.Get("Cc"); cc != "" {
		email.Cc = parseAddressList(cc)
	}

	contentType := msg.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return parseFallbackBody(email, msg.Body), nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		err = parseMultipart(&email, msg.Body, params["boundary"])
	} else {
		email.Body, err = decodePart(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
	}

	return email, err
}

func parseAddressList(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseFallbackBody(email Email, r io.Reader) Email {
	body, _ := io.ReadAll(r)
	email.Body = body
	return email
}

func parseMultipart(email *Email, r io.Reader, boundary string) error {
	mr := multipart.NewReader(r, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if err := processPart(email, part); err != nil {
			return err
		}
	}
	return nil
}

func processPart(email *Email, part *multipart.Part) error {
	contentType := part.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)
	disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")

	if strings.HasPrefix(mediaType, "multipart/") {
		return parseMultipart(email, part, params["boundary"])
	}

	data, err := decodePart(part, part.Header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return err
	}

	filename := dispParams["filename"]
	isAttachment := disposition == "attachment" || disposition == "inline" || filename != ""

	if isAttachment {
		if filename == "" {
			filename = "attachment"
		}
		email.Attachments = append(email.Attachments, Attachment{
			Filename:    filename,
			ContentType: contentType,
			ContentID:   contentID,
			Data:        data,
		})
		return nil
	}

	if mediaType == "text/plain" {
		email.Body = data
		return nil
	}

	if mediaType == "text/html" {
		email.HTMLBody = data
		return nil
	}

	// Other parts (like inline images or unknown types) treat as attachments
	email.Attachments = append(email.Attachments, Attachment{
		Filename:    filename,
		ContentType: contentType,
		ContentID:   contentID,
		Data:        data,
	})
	return nil
}

func decodePart(r io.Reader, encoding string) ([]byte, error) {
	var decoder io.Reader = r
	switch strings.ToLower(encoding) {
	case "base64":
		decoder = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		decoder = quotedprintable.NewReader(r)
	}

	return io.ReadAll(decoder)
}

func generateMessageID(from string) string {
	domain := "gsmail.local"
	if a, err := mail.ParseAddress(from); err == nil {
		parts := strings.Split(a.Address, "@")
		if len(parts) > 1 {
			domain = parts[1]
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("<%x@%s>", b, domain)
}

func formatAddresses(addresses []string) string {
	var formatted []string
	for _, addr := range addresses {
		if a, err := mail.ParseAddress(addr); err == nil {
			formatted = append(formatted, a.String())
		} else {
			formatted = append(formatted, addr)
		}
	}
	return strings.Join(formatted, ", ")
}

func encodeHeader(s string) string {
	// Only encode if contains non-ASCII characters
	for i := range len(s) {
		if s[i] > 127 {
			return mime.QEncoding.Encode("UTF-8", s)
		}
	}
	return s
}

// BuildMessage builds the full RFC822 email message into the provided buffer.
func BuildMessage(bufPtr *[]byte, email Email) {
	writer := NewBufferWriter(bufPtr)
	write := func(s string) {
		_, _ = writer.Write(UnsafeStringToBytes(s))
	}

	writeHeader := func(key, value string) {
		if value != "" && !HasHeader(*bufPtr, key) {
			write(key)
			write(": ")
			write(value)
			write("\r\n")
		}
	}

	// Basic headers
	fromAddr := email.From
	if a, err := mail.ParseAddress(fromAddr); err == nil {
		fromAddr = a.String()
	}
	writeHeader("From", fromAddr)

	if !HasHeader(*bufPtr, "To") && len(email.To) > 0 {
		writeHeader("To", formatAddresses(email.To))
	}

	if len(email.Cc) > 0 {
		writeHeader("Cc", formatAddresses(email.Cc))
	}

	if email.ReplyTo != "" {
		writeHeader("Reply-To", formatAddresses([]string{email.ReplyTo}))
	}

	writeHeader("Subject", encodeHeader(email.Subject))
	writeHeader("MIME-Version", "1.0")

	if !HasHeader(*bufPtr, "Date") {
		writeHeader("Date", time.Now().Format(time.RFC1123Z))
	}

	if !HasHeader(*bufPtr, "Message-ID") {
		writeHeader("Message-ID", generateMessageID(email.From))
	}

	hasAttachments := len(email.Attachments) > 0
	hasBothBodies := len(email.Body) > 0 && len(email.HTMLBody) > 0

	// Determine the main body to use if only one is provided
	mainBody := email.Body
	isHTML := IsHTML(mainBody)
	if len(mainBody) == 0 && len(email.HTMLBody) > 0 {
		mainBody = email.HTMLBody
		isHTML = true
	}

	if !hasAttachments && !hasBothBodies {
		// Simple message
		if !HasHeader(*bufPtr, "Content-Type") {
			if isHTML {
				write(HeaderHTML)
			} else {
				write(HeaderPlain)
			}
			write("\r\n")
		}
		write("\r\n")
		_, _ = writer.Write(mainBody)
		write("\r\n")
		return
	}

	var mw *multipart.Writer

	if hasAttachments {
		mw = multipart.NewWriter(writer)
		writeHeader("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
		write("\r\n")
	} else if hasBothBodies {
		mw = multipart.NewWriter(writer)
		writeHeader("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
		write("\r\n")
	}

	// Write bodies
	if hasBothBodies {
		var amw *multipart.Writer
		if hasAttachments {
			// multipart/alternative inside multipart/mixed
			altHeader := make(textproto.MIMEHeader)
			// We need a new boundary for the alternative part
			tempMw := multipart.NewWriter(io.Discard)
			altBoundary := tempMw.Boundary()
			altHeader.Set("Content-Type", "multipart/alternative; boundary="+altBoundary)
			part, _ := mw.CreatePart(altHeader)
			amw = multipart.NewWriter(part)
			amw.SetBoundary(altBoundary)
		} else {
			amw = mw
		}

		// Plain text part
		textHeader := make(textproto.MIMEHeader)
		textHeader.Set("Content-Type", "text/plain; charset=\"UTF-8\"")
		textHeader.Set("Content-Transfer-Encoding", "base64")
		textPart, _ := amw.CreatePart(textHeader)
		b64Text := base64.NewEncoder(base64.StdEncoding, textPart)
		_, _ = b64Text.Write(email.Body)
		_ = b64Text.Close()

		// HTML part
		htmlHeader := make(textproto.MIMEHeader)
		htmlHeader.Set("Content-Type", "text/html; charset=\"UTF-8\"")
		htmlHeader.Set("Content-Transfer-Encoding", "base64")
		htmlPart, _ := amw.CreatePart(htmlHeader)
		b64HTML := base64.NewEncoder(base64.StdEncoding, htmlPart)
		_, _ = b64HTML.Write(email.HTMLBody)
		_ = b64HTML.Close()

		if hasAttachments {
			_ = amw.Close()
		}
	} else {
		// Single body (either plain or HTML)
		header := make(textproto.MIMEHeader)
		contentType := "text/plain; charset=\"UTF-8\""
		if isHTML {
			contentType = "text/html; charset=\"UTF-8\""
		}
		header.Set("Content-Type", contentType)
		header.Set("Content-Transfer-Encoding", "base64")

		part, _ := mw.CreatePart(header)
		b64 := base64.NewEncoder(base64.StdEncoding, part)
		_, _ = b64.Write(mainBody)
		_ = b64.Close()
	}

	// Attachments
	if hasAttachments {
		for _, att := range email.Attachments {
			attHeader := make(textproto.MIMEHeader)
			attContentType := att.ContentType
			if attContentType == "" {
				attContentType = "application/octet-stream"
			}
			attHeader.Set("Content-Type", attContentType)
			attHeader.Set("Content-Transfer-Encoding", "base64")

			disposition := fmt.Sprintf("attachment; filename=\"%s\"", att.Filename)
			if att.ContentID != "" {
				attHeader.Set("Content-ID", "<"+att.ContentID+">")
				disposition = fmt.Sprintf("inline; filename=\"%s\"", att.Filename)
			}
			attHeader.Set("Content-Disposition", disposition)

			part, _ := mw.CreatePart(attHeader)
			b64Writer := base64.NewEncoder(base64.StdEncoding, part)
			_, _ = b64Writer.Write(att.Data)
			_ = b64Writer.Close()
		}
	}

	if mw != nil {
		_ = mw.Close()
	}
	write("\r\n")
}

package gsmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var emailRegex = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

const (
	// maxBufferSize bounds which pooled buffers are worth keeping. It has to
	// exceed a realistic rendered message or the pool never recycles anything:
	// at 4 KiB even a small HTML mail outgrew it, so every render allocated
	// from scratch and the pool was doing no work at all. Buffers above this
	// (large attachments) are dropped so a single big send does not pin
	// megabytes per P for the life of the process.
	maxBufferSize = 64 << 10

	// initialBufferSize is the starting capacity of a fresh pooled buffer,
	// chosen to cover the header block plus a small body without regrowing.
	initialBufferSize = 4096

	// maxPartSize bounds a single decoded MIME part when parsing an inbound
	// message, so a hostile sender cannot exhaust memory.
	maxPartSize = 25 << 20

	// maxMultipartDepth bounds multipart nesting when parsing an inbound
	// message, so a hostile sender cannot exhaust the stack.
	maxMultipartDepth = 10
)

// ErrPartTooLarge is returned when a MIME part exceeds maxPartSize.
var ErrPartTooLarge = errors.New("gsmail: mime part exceeds maximum size")

// ErrTooDeeplyNested is returned when a message nests multipart containers
// beyond maxMultipartDepth.
var ErrTooDeeplyNested = errors.New("gsmail: mime message is nested too deeply")

// htmlTag is a marker this package treats as evidence of HTML.
//
// heading marks <h, which must be followed by a digit: matching the bare
// prefix classified "Temperature dropped <halfway>" as HTML.
type htmlTag struct {
	prefix  []byte
	heading bool
}

var (
	htmlTags = []htmlTag{
		{prefix: []byte("<html")},
		{prefix: []byte("<body")},
		{prefix: []byte("<div")},
		{prefix: []byte("<p")},
		{prefix: []byte("<!doctype")},
		{prefix: []byte("<h"), heading: true},
	}

	bufferPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, initialBufferSize)
			return &b
		},
	}
)

// getBuffer retrieves a byte slice from the pool.
func getBuffer() *[]byte {
	return bufferPool.Get().(*[]byte)
}

// putBuffer returns a byte slice to the pool if it's within the size limit.
func putBuffer(b *[]byte) {
	if b == nil {
		return
	}
	if cap(*b) <= maxBufferSize {
		*b = (*b)[:0]
		bufferPool.Put(b)
	}
}

// newBufferWriter creates a new bufferWriter for the given buffer.
func newBufferWriter(b *[]byte) *bufferWriter {
	return &bufferWriter{bufPtr: b}
}

// bufferWriter implements io.Writer for the pooled byte slices.
type bufferWriter struct {
	bufPtr *[]byte
}

// Write appends data to the underlying buffer.
func (w *bufferWriter) Write(p []byte) (n int, err error) {
	*w.bufPtr = append(*w.bufPtr, p...)
	return len(p), nil
}

// hasHeader reports whether b contains the given header, searching only the
// header section (before the first double newline).
func hasHeader(b []byte, header string) bool {
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

	headerBytes := unsafeStringToBytes(header)

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
		if containsTag(prefix, tag) {
			return true
		}
	}
	return false
}

// containsTag reports whether b holds tag as an actual tag rather than as an
// incidental substring.
//
// Matching the bare prefix meant ordinary prose was classified as HTML:
// "Please review <p1> pricing" matched "<p", and the message then went out as
// text/html, where the recipient's renderer swallowed "<p1>" and the sentence
// lost a word. Requiring a character that may legally follow a tag name --
// and a digit after <h -- costs nothing and removes that whole class.
func containsTag(b []byte, tag htmlTag) bool {
	n := len(tag.prefix)
	if n == 0 || len(b) < n {
		return false
	}
	for i := 0; i+n <= len(b); i++ {
		if !matchAt(b, i, tag.prefix) {
			continue
		}
		j := i + n
		if tag.heading {
			if j >= len(b) || b[j] < '1' || b[j] > '6' {
				continue
			}
			j++
		}
		if j < len(b) && isTagTerminator(b[j]) {
			return true
		}
	}
	return false
}

// isTagTerminator reports whether c may follow a tag name.
func isTagTerminator(c byte) bool {
	switch c {
	case '>', '/', ' ', '\t', '\r', '\n':
		return true
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

// unsafeStringToBytes converts a string to a byte slice without allocation.
// The caller must not modify the returned byte slice.
func unsafeStringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// unsafeBytesToString converts a byte slice to a string without allocation.
func unsafeBytesToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// defaultDisposableDomains is the built-in set of throwaway mail providers.
// It is deliberately small and will go stale; supply a maintained list through
// Validator.DisposableDomains instead of relying on it in production.
var defaultDisposableDomains = map[string]struct{}{
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

// DefaultDisposableDomains returns a copy of the built-in disposable domain
// set, so callers can extend it without mutating package state.
func DefaultDisposableDomains() map[string]struct{} {
	out := make(map[string]struct{}, len(defaultDisposableDomains))
	for d := range defaultDisposableDomains {
		out[d] = struct{}{}
	}
	return out
}

func isDisposableDomain(domain string, set map[string]struct{}) bool {
	if set == nil {
		set = defaultDisposableDomains
	}
	_, exists := set[strings.ToLower(domain)]
	return exists
}

// IsDisposableEmail reports whether the address belongs to a known throwaway
// provider from the built-in list. Use Validator.DisposableDomains to supply
// your own list.
func IsDisposableEmail(email string) bool {
	i := strings.LastIndexByte(email, '@')
	if i < 1 || i >= len(email)-1 {
		return false
	}
	return isDisposableDomain(email[i+1:], nil)
}

// IsValidEmail checks if the given string is a valid email address.
// It uses a fast regex check and common sense length limits.
func IsValidEmail(email string) bool {
	if len(email) < 3 || len(email) > 254 {
		return false
	}
	return emailRegex.MatchString(strings.ToLower(email))
}

// Validation errors. They are all non-retryable: retrying will not change the
// verdict for a malformed or disallowed address.
var (
	ErrInvalidEmailFormat = errors.New("gsmail: invalid email format")
	ErrDisposableEmail    = errors.New("gsmail: disposable/temporary email address not allowed")
	ErrNoMXRecords        = errors.New("gsmail: no MX records found for domain")
)

// Resolver abstracts the DNS lookups used for validation and domain health
// checks. *net.Resolver satisfies this interface.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Validator validates email addresses.
//
// The zero value is ready to use and performs offline checks only: syntax and
// the disposable-domain list. Network checks are opt-in.
//
// CheckMailbox performs an SMTP RCPT probe ("callback verification") against
// the recipient's MX host. Enable it only if you understand the trade-offs:
// outbound port 25 is blocked by most cloud providers, many operators treat
// probing as abuse and will greylist or blocklist the source, and catch-all
// domains accept every recipient, so a "valid" result means nothing.
type Validator struct {
	// CheckMX enables an MX record lookup for the address domain.
	CheckMX bool

	// CheckMailbox enables the SMTP RCPT probe. It implies CheckMX.
	CheckMailbox bool

	// AllowDisposable disables the disposable-domain rejection.
	AllowDisposable bool

	// DisposableDomains overrides the built-in disposable domain set.
	// Keys must be lowercase.
	DisposableDomains map[string]struct{}

	// Resolver performs DNS lookups. Defaults to net.DefaultResolver.
	Resolver Resolver

	// Dialer dials the MX host for the RCPT probe. Defaults to a dialer with
	// a five second timeout.
	Dialer *net.Dialer

	// SMTPPort is the port used for the RCPT probe. Defaults to "25".
	SMTPPort string

	// HeloName is sent in the EHLO/HELO command. Defaults to "localhost".
	HeloName string

	// MailFrom is the reverse path used for the probe. Defaults to the null
	// reverse path ("<>"), which is the conventional choice for probes.
	MailFrom string
}

func (v *Validator) resolver() Resolver {
	if v.Resolver != nil {
		return v.Resolver
	}
	return net.DefaultResolver
}

func (v *Validator) dialer() *net.Dialer {
	if v.Dialer != nil {
		return v.Dialer
	}
	return &net.Dialer{Timeout: 5 * time.Second}
}

func (v *Validator) port() string {
	if v.SMTPPort != "" {
		return v.SMTPPort
	}
	return "25"
}

func (v *Validator) helo() string {
	if v.HeloName != "" {
		return v.HeloName
	}
	return "localhost"
}

// Validate checks the address according to the validator's configuration.
func (v *Validator) Validate(ctx context.Context, email string) error {
	if !IsValidEmail(email) {
		return NonRetryable(ErrInvalidEmailFormat)
	}

	i := strings.LastIndexByte(email, '@')
	domain := email[i+1:]

	if !v.AllowDisposable && isDisposableDomain(domain, v.DisposableDomains) {
		return NonRetryable(ErrDisposableEmail)
	}

	if !v.CheckMX && !v.CheckMailbox {
		return nil
	}

	mxs, err := v.resolver().LookupMX(ctx, domain)
	if err != nil {
		return fmt.Errorf("lookup mx for %q: %w", domain, err)
	}
	if len(mxs) == 0 {
		return NonRetryable(fmt.Errorf("%w: %s", ErrNoMXRecords, domain))
	}

	if !v.CheckMailbox {
		return nil
	}

	var lastErr error
	for _, mx := range mxs {
		addr := net.JoinHostPort(mx.Host, v.port())
		if err := v.probe(ctx, addr, email); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("could not verify mailbox %q: %w", email, lastErr)
}

func (v *Validator) probe(ctx context.Context, addr, email string) error {
	conn, err := v.dialer().DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

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
		func() error { return client.Hello(v.helo()) },
		func() error { return client.Mail(v.MailFrom) },
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

// ValidateEmailSyntax performs the offline checks only: syntax and the
// built-in disposable domain list. It never touches the network.
func ValidateEmailSyntax(email string) error {
	var v Validator
	return v.Validate(context.Background(), email)
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
		err = parseMultipart(&email, msg.Body, params["boundary"], 0)
	} else {
		email.Body, err = decodePart(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
	}

	return email, err
}

// parseAddressList splits an address header into individual addresses.
// It uses the RFC 5322 parser so that quoted display names containing commas
// (for example `"Doe, John" <j@example.com>`) are not split apart.
func parseAddressList(s string) []string {
	if addrs, err := mail.ParseAddressList(s); err == nil {
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			if a.Name == "" {
				out = append(out, a.Address)
				continue
			}
			out = append(out, a.String())
		}
		return out
	}

	// Fall back to a naive split for malformed headers, but never emit empties.
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseFallbackBody(email Email, r io.Reader) Email {
	body, _ := readLimited(r)
	email.Body = body
	return email
}

// readLimited reads at most maxPartSize bytes and reports ErrPartTooLarge if
// the source has more to give.
func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxPartSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPartSize {
		return nil, ErrPartTooLarge
	}
	return b, nil
}

func parseMultipart(email *Email, r io.Reader, boundary string, depth int) error {
	if depth >= maxMultipartDepth {
		return ErrTooDeeplyNested
	}
	if boundary == "" {
		return fmt.Errorf("multipart content type without boundary")
	}

	mr := multipart.NewReader(r, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		err = processPart(email, part, depth)
		_ = part.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func processPart(email *Email, part *multipart.Part, depth int) error {
	contentType := part.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)
	disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")

	if strings.HasPrefix(mediaType, "multipart/") {
		return parseMultipart(email, part, params["boundary"], depth+1)
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
	// Bound the *encoded* stream too: base64 and quoted-printable both expand,
	// so limiting only the decoded output would still let a small hostile
	// message force a large read.
	var decoder io.Reader = io.LimitReader(r, 2*maxPartSize)
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		decoder = base64.NewDecoder(base64.StdEncoding, decoder)
	case "quoted-printable":
		decoder = quotedprintable.NewReader(decoder)
	}

	return readLimited(decoder)
}

// messageIDDomain extracts the right-hand side of the From address for use in
// a generated Message-ID.
//
// It scans for the domain rather than calling mail.ParseAddress, which would
// re-parse the very same From that FormatAddress already parsed. RFC 5322
// address parsing is the largest single allocation source in rendering a
// message, and doing it twice for one field bought nothing. The result is
// validated below, so a malformed or hostile From can only yield a safe
// domain or the fallback.
func messageIDDomain(from string) string {
	const fallback = "gsmail.local"

	s := strings.TrimSpace(from)

	// Prefer the angle-addr when the value is "Display Name <a@b.test>", so a
	// display name containing an @ cannot supply the domain.
	if i := strings.LastIndexByte(s, '<'); i >= 0 {
		j := strings.IndexByte(s[i:], '>')
		if j <= 1 {
			return fallback
		}
		s = s[i+1 : i+j]
	} else if strings.ContainsFunc(s, isAddrSeparator) {
		// With no angle-addr this must be a bare addr-spec. Anything carrying
		// whitespace or a control character is not one, and scanning it for
		// the last @ would happily read a domain out of text that follows a
		// header break -- "a@good.test\r\nBcc: x@evil.test" would yield
		// evil.test. Refuse the whole value instead.
		return fallback
	}

	i := strings.LastIndexByte(s, '@')
	if i < 0 || i == len(s)-1 {
		return fallback
	}
	if !isSafeMessageIDDomain(s[i+1:]) {
		return fallback
	}
	return s[i+1:]
}

// isAddrSeparator reports whether r cannot appear inside a bare addr-spec.
func isAddrSeparator(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r < 0x20 || r == 0x7f
}

// isSafeMessageIDDomain reports whether domain may be placed inside a
// Message-ID unescaped. Anything outside the LDH set is rejected outright, so
// no separator, control character or header break can reach the wire.
func isSafeMessageIDDomain(domain string) bool {
	if domain == "" || len(domain) > 255 {
		return false
	}
	for i := 0; i < len(domain); i++ {
		c := domain[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}

func generateMessageID(from string) string {
	domain := messageIDDomain(from)

	var b [16]byte
	_, _ = rand.Read(b[:])

	// Assembled by hand rather than with fmt.Sprintf, which showed up in the
	// allocation profile of every rendered message.
	out := make([]byte, 0, 1+32+1+len(domain)+1)
	out = append(out, '<')
	out = hex.AppendEncode(out, b[:])
	out = append(out, '@')
	out = append(out, domain...)
	out = append(out, '>')
	return unsafeBytesToString(out)
}

// FormatAddresses formats a list of addresses into a single string for headers.
func FormatAddresses(addresses []string) string {
	return strings.Join(FormatAddressList(addresses), ", ")
}

// FormatAddressList formats a list of addresses into a slice of strings.
// Entries that are empty or that contain a line break are dropped: such a
// value cannot be represented in a single header field and is almost always a
// header-injection attempt.
func FormatAddressList(addresses []string) []string {
	formatted := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		if f := FormatAddress(addr); f != "" {
			formatted = append(formatted, f)
		}
	}
	return formatted
}

// FormatAddress ensures an email address is properly formatted (e.g., quotes
// names with special characters). It returns "" for an address containing CR
// or LF, so that untrusted input cannot inject additional headers.
func FormatAddress(s string) string {
	if strings.ContainsAny(s, "\r\n") {
		return ""
	}
	s = sanitizeHeaderValue(s)
	if a, err := ParseEmailAddress(s); err == nil && a != nil {
		if a.Name == "" {
			return a.Address
		}
		return a.String()
	}
	return s
}

// ParseEmailAddress parses an email address that can be in the form of "Name <email@example.com>" or just "email@example.com".
func ParseEmailAddress(s string) (*mail.Address, error) {
	if s == "" {
		return nil, nil
	}
	a, err := mail.ParseAddress(s)
	if err == nil {
		return a, nil
	}

	// Try manual parsing if standard parser fails (e.g., name with comma not quoted)
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ">") {
		idx := strings.LastIndex(s, "<")
		if idx >= 0 {
			name := strings.TrimSpace(s[:idx])
			email := strings.TrimSpace(s[idx+1 : len(s)-1])
			// If name is quoted, unquote it so a.String() can quote it properly later
			if name != "" && len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
				name = name[1 : len(name)-1]
			}
			return &mail.Address{Name: name, Address: email}, nil
		}
	}

	return nil, err
}

// SanitizeHeaderValue strips every character that must not appear in an
// RFC 5322 header field value: CR, LF, the remaining C0 controls and DEL.
// Apply it to any untrusted string before putting it into a header.
func SanitizeHeaderValue(s string) string {
	return sanitizeHeaderValue(s)
}

func sanitizeHeaderValue(s string) string {
	if !strings.ContainsFunc(s, isIllegalHeaderRune) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isIllegalHeaderRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isIllegalHeaderRune(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
}

// encodeHeader prepares an arbitrary string for use as a header field value.
// The value is sanitised first, so control characters can never reach the
// wire, and is then RFC 2047 encoded whenever it is not plain printable ASCII.
func encodeHeader(s string) string {
	s = sanitizeHeaderValue(s)
	if s == "" {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return mime.QEncoding.Encode("UTF-8", s)
		}
	}
	return s
}

// reservedHeaders are produced by buildMessage itself. Supplying them through
// Email.Headers would duplicate or corrupt the generated message.
var reservedHeaders = map[string]struct{}{
	"from":                      {},
	"to":                        {},
	"cc":                        {},
	"bcc":                       {},
	"reply-to":                  {},
	"subject":                   {},
	"mime-version":              {},
	"content-type":              {},
	"content-transfer-encoding": {},
}

// CustomHeaders returns the caller-supplied headers a provider may pass to its
// own API, with reserved names dropped, names validated and values sanitised
// and RFC 2047 encoded.
//
// Providers that construct a message through a vendor API instead of through
// buildMessage use this so Email.Headers means the same thing on every
// transport. Without it, List-Unsubscribe — which Gmail and Yahoo require from
// bulk senders — is silently lost on the API providers while working fine over
// SMTP.
//
// It reports the same error buildMessage would for an illegal header name,
// rather than quietly skipping it, so a mistake surfaces on every transport.
func CustomHeaders(h map[string]string) (map[string]string, error) {
	if len(h) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(h))
	for _, name := range sortedHeaderNames(h) {
		if _, reserved := reservedHeaders[strings.ToLower(name)]; reserved {
			continue
		}
		if !isValidHeaderName(name) {
			return nil, NonRetryable(fmt.Errorf("gsmail: invalid header name %q", name))
		}
		if v := encodeHeader(h[name]); v != "" {
			out[name] = v
		}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// isValidHeaderName reports whether name is a legal RFC 5322 field name.
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7f || c == ':' {
			return false
		}
	}
	return true
}

// formatDisposition builds a Content-Disposition value.
//
// The file name is always emitted as a quoted ASCII string, and a RFC 2231
// (RFC 5987) extended form is appended when the name is not plain ASCII.
// mime.FormatMediaType is deliberately not used here: it emits a bare token
// for simple names ("filename=image.png") and *only* the extended form for
// non-ASCII ones. Several clients, older Outlook in particular, mishandle both
// and fall back to "ATT00001.dat", so we always give them a quoted name they
// understand and let capable clients pick up filename* instead.
func formatDisposition(kind, filename string) string {
	filename = sanitizeHeaderValue(filename)
	if filename == "" {
		return kind
	}

	var b strings.Builder
	b.WriteString(kind)
	b.WriteString(`; filename="`)
	b.WriteString(quoteFilename(asciiFilename(filename)))
	b.WriteByte('"')

	if !isPlainASCII(filename) {
		b.WriteString("; filename*=utf-8''")
		b.WriteString(rfc2231Escape(filename))
	}
	return b.String()
}

// isPlainASCII reports whether s consists only of printable ASCII.
func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// asciiFilename replaces every non-printable-ASCII rune with an underscore so
// the quoted fallback is representable in a plain header.
func asciiFilename(s string) string {
	if isPlainASCII(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quoteFilename escapes the two characters that may not appear unescaped
// inside an RFC 5322 quoted-string.
func quoteFilename(s string) string {
	if !strings.ContainsAny(s, `"\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// rfc2231Escape percent-encodes s for use in an extended parameter value,
// leaving only the attr-char set of RFC 5987 untouched.
func rfc2231Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// isAttrChar reports whether c is an RFC 5987 attr-char.
func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}

// crlf is shared rather than built inline. The line break is emitted once per
// 76 base64 characters, and `[]byte("\r\n")` inside that loop escapes into the
// io.Writer call and heap-allocates every time: it was the single largest
// source of allocations when rendering a message.
var crlf = []byte("\r\n")

type base64MIMEWriter struct {
	w   io.Writer
	col int
}

func (w *base64MIMEWriter) Write(p []byte) (int, error) {
	written := 0

	for len(p) > 0 {
		if w.col == 76 {
			if _, err := w.w.Write(crlf); err != nil {
				return written, err
			}
			w.col = 0
		}

		n := min(76-w.col, len(p))
		nn, err := w.w.Write(p[:n])
		written += nn
		w.col += nn
		if err != nil {
			return written, err
		}
		if nn < n {
			return written, io.ErrShortWrite
		}

		p = p[n:]
	}

	return written, nil
}

func writeMIMEBase64(w io.Writer, b []byte) error {
	wrapped := &base64MIMEWriter{w: w}
	enc := base64.NewEncoder(base64.StdEncoding, wrapped)
	if _, err := enc.Write(b); err != nil {
		_ = enc.Close()
		return err
	}
	return enc.Close()
}

// ErrConflictingContentType is returned when rendering a message and the caller has
// already written a Content-Type header but the message needs a multipart
// container. Emitting the body anyway would produce an unparseable message.
var ErrConflictingContentType = errors.New("gsmail: Content-Type already set but a multipart body is required")

// RenderMessage renders email as a complete RFC 5322 message.
//
// Every header value is sanitised: CR and LF are stripped, non-ASCII text is
// RFC 2047 encoded and attachment file names are encoded per RFC 2231, so a
// subject, recipient or file name coming from untrusted input cannot inject
// additional headers.
func RenderMessage(email Email) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	if err := buildMessage(&buf, email); err != nil {
		return nil, err
	}
	return buf, nil
}

// WithMessage renders email into a pooled buffer and calls fn with the result.
// The slice passed to fn is only valid until fn returns; copy it if you need
// to keep it. Prefer this over RenderMessage on hot paths.
func WithMessage(email Email, fn func(msg []byte) error) error {
	bufPtr := getBuffer()
	defer putBuffer(bufPtr)

	if err := buildMessage(bufPtr, email); err != nil {
		return err
	}
	return fn(*bufPtr)
}

// buildMessage builds the full RFC 5322 email message into the provided
// buffer. See RenderMessage for the sanitisation guarantees.
//
// It is unexported: the *[]byte parameter exists so a pooled buffer can be
// reused, which is an implementation detail rather than an API. RenderMessage
// and WithMessage express the two things a caller actually wants -- a message
// they keep, and a message they only read.
func buildMessage(bufPtr *[]byte, email Email) error {
	writer := newBufferWriter(bufPtr)
	var werr error

	// Whatever the caller already had in the buffer. RenderMessage and
	// WithMessage both start from empty, so this is normally zero and every
	// prefix check below short-circuits.
	//
	// writeHeader used to call HasHeader against the *whole growing buffer* on
	// every single header, which made header emission quadratic: 64 custom
	// headers cost 12x a bare message. Only the caller's prefix can hold a
	// header we did not write ourselves, so that is all we scan; duplicates of
	// our own output are tracked with the flags below.
	prefixLen := len(*bufPtr)
	hasPrefixHeader := func(key string) bool {
		if prefixLen == 0 {
			return false
		}
		return hasHeader((*bufPtr)[:prefixLen], key)
	}

	write := func(s string) {
		if werr != nil {
			return
		}
		_, werr = writer.Write(unsafeStringToBytes(s))
	}

	writeHeader := func(key, value string) {
		if value == "" || hasPrefixHeader(key) {
			return
		}
		write(key)
		write(": ")
		write(value)
		write("\r\n")
	}

	// Basic headers
	writeHeader("From", FormatAddress(email.From))

	if len(email.To) > 0 {
		writeHeader("To", FormatAddresses(email.To))
	}

	if len(email.Cc) > 0 {
		writeHeader("Cc", FormatAddresses(email.Cc))
	}

	// Bcc is deliberately never written: recipients are carried in the
	// envelope only, so they stay hidden from every recipient.

	if email.ReplyTo != "" {
		writeHeader("Reply-To", FormatAddresses([]string{email.ReplyTo}))
	}

	writeHeader("Subject", encodeHeader(email.Subject))
	writeHeader("MIME-Version", "1.0")

	// Date and Message-ID are generated here but are not in reservedHeaders,
	// so a caller may also supply them through Email.Headers. Track what we
	// emitted so the loop below does not append a second copy.
	var wroteDate, wroteMessageID bool
	if !hasPrefixHeader("Date") {
		writeHeader("Date", time.Now().Format(time.RFC1123Z))
		wroteDate = true
	}

	if !hasPrefixHeader("Message-ID") {
		writeHeader("Message-ID", generateMessageID(email.From))
		wroteMessageID = true
	}

	// Caller supplied headers (List-Unsubscribe, In-Reply-To, X-*, ...).
	for _, name := range sortedHeaderNames(email.Headers) {
		lower := strings.ToLower(name)
		if _, reserved := reservedHeaders[lower]; reserved {
			continue
		}
		if (lower == "date" && wroteDate) || (lower == "message-id" && wroteMessageID) {
			continue
		}
		if !isValidHeaderName(name) {
			// Permanent: the name will not become legal on a retry. Marked
			// explicitly so the SMTP and SES paths agree with CustomHeaders,
			// which the API-backed providers use.
			return NonRetryable(fmt.Errorf("gsmail: invalid header name %q", name))
		}
		writeHeader(name, encodeHeader(email.Headers[name]))
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
		// Simple message - use base64 encoding so Unicode (emoji, etc.) is preserved through transport and Outlook
		if !hasPrefixHeader("Content-Type") {
			if isHTML {
				write(HeaderHTML)
			} else {
				write(HeaderPlain)
			}
			write("\r\n")
		}
		if !hasPrefixHeader("Content-Transfer-Encoding") {
			write("Content-Transfer-Encoding: base64\r\n")
		}
		write("\r\n")
		if werr != nil {
			return werr
		}
		if err := writeMIMEBase64(writer, mainBody); err != nil {
			return err
		}
		write("\r\n")
		return werr
	}

	// A multipart body needs the boundary we are about to generate. If the
	// caller already pinned a Content-Type we cannot express that, and writing
	// the parts anyway would yield an unparseable message.
	if hasPrefixHeader("Content-Type") {
		// Also permanent: the caller pinned a Content-Type that cannot
		// coexist with the multipart body this message needs.
		return NonRetryable(ErrConflictingContentType)
	}

	mw := multipart.NewWriter(writer)
	if hasAttachments {
		writeHeader("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	} else {
		writeHeader("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	}
	write("\r\n")
	if werr != nil {
		return werr
	}

	// Write bodies
	if hasBothBodies {
		amw := mw
		if hasAttachments {
			// multipart/alternative inside multipart/mixed
			altHeader := make(textproto.MIMEHeader)
			// We need a new boundary for the alternative part
			altBoundary := multipart.NewWriter(io.Discard).Boundary()
			altHeader.Set("Content-Type", "multipart/alternative; boundary="+altBoundary)
			part, err := mw.CreatePart(altHeader)
			if err != nil {
				return err
			}
			amw = multipart.NewWriter(part)
			if err := amw.SetBoundary(altBoundary); err != nil {
				return err
			}
		}

		if err := writeBodyPart(amw, "text/plain", email.Body); err != nil {
			return err
		}
		if err := writeBodyPart(amw, "text/html", email.HTMLBody); err != nil {
			return err
		}

		if hasAttachments {
			if err := amw.Close(); err != nil {
				return err
			}
		}
	} else {
		contentType := "text/plain"
		if isHTML {
			contentType = "text/html"
		}
		if err := writeBodyPart(mw, contentType, mainBody); err != nil {
			return err
		}
	}

	// Attachments
	for _, att := range email.Attachments {
		if err := writeAttachmentPart(mw, att); err != nil {
			return err
		}
	}

	if err := mw.Close(); err != nil {
		return err
	}
	write("\r\n")
	return werr
}

func writeBodyPart(mw *multipart.Writer, mediaType string, body []byte) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", mediaType+"; charset=\"UTF-8\"")
	header.Set("Content-Transfer-Encoding", "base64")

	part, err := mw.CreatePart(header)
	if err != nil {
		return err
	}
	return writeMIMEBase64(part, body)
}

func writeAttachmentPart(mw *multipart.Writer, att Attachment) error {
	header := make(textproto.MIMEHeader)

	contentType := sanitizeHeaderValue(att.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "base64")

	kind := "attachment"
	if att.ContentID != "" {
		// Assigned directly rather than through Set, which canonicalises the
		// key to "Content-Id". Both are legal — header names are
		// case-insensitive — but "Content-ID" is the spelling every example
		// and every other mailer uses, and some older clients match on it
		// literally when resolving cid: references.
		header["Content-ID"] = []string{"<" + sanitizeHeaderValue(att.ContentID) + ">"}
		kind = "inline"
	}
	header.Set("Content-Disposition", formatDisposition(kind, att.Filename))

	part, err := mw.CreatePart(header)
	if err != nil {
		return err
	}
	return writeMIMEBase64(part, att.Data)
}

// sortedHeaderNames returns the map keys in a stable order so that rendering
// the same Email twice produces byte-identical output (important for DKIM).
func sortedHeaderNames(h map[string]string) []string {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

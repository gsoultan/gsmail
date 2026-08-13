package gsmail

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// HealthResult represents the outcome of a single DNS health check.
type HealthResult struct {
	Found   bool   `json:"found"`
	Valid   bool   `json:"valid"`
	Record  string `json:"record,omitempty"`
	Details string `json:"details,omitempty"`
	Error   string `json:"error,omitempty"`
}

// DomainHealth aggregates all DNS health checks for a domain.
type DomainHealth struct {
	Domain string                  `json:"domain"`
	SPF    HealthResult            `json:"spf"`
	DMARC  HealthResult            `json:"dmarc"`
	DKIM   map[string]HealthResult `json:"dkim"`
	MX     HealthResult            `json:"mx"`
}

// HealthChecker performs DNS-based domain health checks. The zero value is
// ready to use and resolves through net.DefaultResolver.
type HealthChecker struct {
	// Resolver performs the DNS lookups. Defaults to net.DefaultResolver.
	Resolver Resolver
}

func (h HealthChecker) resolver() Resolver {
	if h.Resolver != nil {
		return h.Resolver
	}
	return net.DefaultResolver
}

// CheckDomainHealth performs comprehensive DNS health checks for the given domain.
func CheckDomainHealth(ctx context.Context, domain string, selectors []string) (DomainHealth, error) {
	return HealthChecker{}.CheckDomainHealth(ctx, domain, selectors)
}

// CheckMX retrieves the MX records for a domain.
func CheckMX(ctx context.Context, domain string) HealthResult {
	return HealthChecker{}.CheckMX(ctx, domain)
}

// CheckSPF retrieves and validates the SPF record for a domain.
func CheckSPF(ctx context.Context, domain string) HealthResult {
	return HealthChecker{}.CheckSPF(ctx, domain)
}

// CheckDMARC retrieves and validates the DMARC record for a domain.
func CheckDMARC(ctx context.Context, domain string) HealthResult {
	return HealthChecker{}.CheckDMARC(ctx, domain)
}

// CheckDKIM retrieves and validates a DKIM record for a domain and selector.
func CheckDKIM(ctx context.Context, domain, selector string) HealthResult {
	return HealthChecker{}.CheckDKIM(ctx, domain, selector)
}

// CheckDomainHealth performs comprehensive DNS health checks for the given domain.
func (h HealthChecker) CheckDomainHealth(ctx context.Context, domain string, selectors []string) (DomainHealth, error) {
	if domain == "" {
		return DomainHealth{}, fmt.Errorf("domain is required")
	}

	health := DomainHealth{
		Domain: domain,
		DKIM:   make(map[string]HealthResult),
	}

	type result struct {
		typ      string
		selector string
		res      HealthResult
	}

	resChan := make(chan result)
	var wg sync.WaitGroup

	// Check MX
	wg.Add(1)
	go func() {
		defer wg.Done()
		res := h.CheckMX(ctx, domain)
		select {
		case resChan <- result{typ: "mx", res: res}:
		case <-ctx.Done():
		}
	}()

	// Check SPF
	wg.Add(1)
	go func() {
		defer wg.Done()
		res := h.CheckSPF(ctx, domain)
		select {
		case resChan <- result{typ: "spf", res: res}:
		case <-ctx.Done():
		}
	}()

	// Check DMARC
	wg.Add(1)
	go func() {
		defer wg.Done()
		res := h.CheckDMARC(ctx, domain)
		select {
		case resChan <- result{typ: "dmarc", res: res}:
		case <-ctx.Done():
		}
	}()

	// Check DKIM
	for _, selector := range selectors {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			res := h.CheckDKIM(ctx, domain, s)
			select {
			case resChan <- result{typ: "dkim", selector: s, res: res}:
			case <-ctx.Done():
			}
		}(selector)
	}

	// Closer goroutine
	go func() {
		wg.Wait()
		close(resChan)
	}()

	// Collect results
	for {
		select {
		case <-ctx.Done():
			return health, ctx.Err()
		case r, ok := <-resChan:
			if !ok {
				return health, nil
			}
			switch r.typ {
			case "mx":
				health.MX = r.res
			case "spf":
				health.SPF = r.res
			case "dmarc":
				health.DMARC = r.res
			case "dkim":
				health.DKIM[r.selector] = r.res
			}
		}
	}
}

// CheckMX retrieves the MX records for a domain.
//
// A domain that does not exist reports Found false rather than an error, which
// is what CheckSPF and CheckDMARC already did. The MX check used to treat it
// as an error, so the same missing domain produced a "not found" verdict from
// two checks and a failure from the third -- which reads as an outage rather
// than as an unconfigured domain.
func (h HealthChecker) CheckMX(ctx context.Context, domain string) HealthResult {
	mxs, err := h.resolver().LookupMX(ctx, domain)
	if err != nil {
		if isNotFound(err) {
			return HealthResult{Found: false, Details: "No MX records found"}
		}
		return HealthResult{Error: err.Error()}
	}
	if len(mxs) == 0 {
		return HealthResult{Found: false, Details: "No MX records found"}
	}

	hosts := make([]string, 0, len(mxs))
	for _, mx := range mxs {
		hosts = append(hosts, fmt.Sprintf("%s (%d)", mx.Host, mx.Pref))
	}
	return HealthResult{
		Found:  true,
		Valid:  true,
		Record: strings.Join(hosts, ", "),
	}
}

// CheckSPF retrieves and validates the SPF record for a domain.
func (h HealthChecker) CheckSPF(ctx context.Context, domain string) HealthResult {
	txts, err := h.resolver().LookupTXT(ctx, domain)
	if err != nil {
		// Ignore "no such host" or similar as just "not found"
		if isNotFound(err) {
			return HealthResult{Found: false, Details: "No TXT records found"}
		}
		return HealthResult{Error: err.Error()}
	}

	var spfRecords []string
	for _, txt := range txts {
		cleanTxt := strings.TrimSpace(txt)
		if strings.HasPrefix(strings.ToLower(cleanTxt), "v=spf1") {
			spfRecords = append(spfRecords, cleanTxt)
		}
	}

	if len(spfRecords) == 0 {
		return HealthResult{Found: false, Details: "No SPF record found"}
	}

	if len(spfRecords) > 1 {
		return HealthResult{
			Found:   true,
			Valid:   false,
			Record:  strings.Join(spfRecords, " | "),
			Details: "Multiple SPF records found (invalid configuration)",
		}
	}

	return HealthResult{
		Found:  true,
		Valid:  true,
		Record: spfRecords[0],
	}
}

// CheckDMARC retrieves and validates the DMARC record for a domain.
func (h HealthChecker) CheckDMARC(ctx context.Context, domain string) HealthResult {
	dmarcDomain := "_dmarc." + domain
	txts, err := h.resolver().LookupTXT(ctx, dmarcDomain)
	if err != nil {
		if isNotFound(err) {
			return HealthResult{Found: false, Details: "No DMARC record found"}
		}
		return HealthResult{Error: err.Error()}
	}

	var dmarcRecords []string
	for _, txt := range txts {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(txt)), "V=DMARC1") {
			dmarcRecords = append(dmarcRecords, txt)
		}
	}

	if len(dmarcRecords) == 0 {
		return HealthResult{Found: false, Details: "No DMARC record found"}
	}

	if len(dmarcRecords) > 1 {
		return HealthResult{
			Found:   true,
			Valid:   false,
			Record:  strings.Join(dmarcRecords, " | "),
			Details: "Multiple DMARC records found (invalid configuration)",
		}
	}

	return HealthResult{
		Found:  true,
		Valid:  true,
		Record: dmarcRecords[0],
	}
}

// CheckDKIM retrieves and validates a DKIM record for a domain and selector.
func (h HealthChecker) CheckDKIM(ctx context.Context, domain, selector string) HealthResult {
	if selector == "" {
		return HealthResult{Error: "Selector is required for DKIM check"}
	}

	dkimDomain := selector + "._domainkey." + domain
	txts, err := h.resolver().LookupTXT(ctx, dkimDomain)
	if err != nil {
		if isNotFound(err) {
			return HealthResult{Found: false, Details: "No DKIM record found for selector " + selector}
		}
		return HealthResult{Error: err.Error()}
	}

	if len(txts) == 0 {
		return HealthResult{Found: false, Details: "No DKIM record found for selector " + selector}
	}

	if len(txts) > 1 {
		return HealthResult{
			Found:   true,
			Valid:   false,
			Record:  strings.Join(txts, " | "),
			Details: "Multiple DKIM records found for selector " + selector + " (invalid configuration)",
		}
	}

	record := txts[0]

	// Simple tag parser
	tags := make(map[string]string)
	parts := strings.Split(record, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			tags[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}

	valid := true
	details := ""
	if p, ok := tags["p"]; !ok {
		valid = false
		details = "DKIM record missing 'p=' tag (public key)"
	} else if p == "" {
		valid = false
		details = "DKIM public key has been revoked (p= is empty)"
	}

	return HealthResult{
		Found:   true,
		Valid:   valid,
		Record:  record,
		Details: details,
	}
}

// CheckDKIMKey verifies that the DKIM record published for a selector carries
// the public half of the private key you sign with.
//
// CheckDKIM answers "is a well-formed record published?". That is a weaker
// question than it looks, because a record can be perfectly valid and belong
// to a key you retired months ago. Every message you send is then signed with
// a key nobody publishes, and every receiver records a DKIM failure -- which is
// a worse signal than not signing at all, and is invisible until a
// deliverability report arrives.
//
// This is the check that catches a rotation applied in one place and not the
// other. Pass the same key you give to DKIMOptions.
func (h HealthChecker) CheckDKIMKey(ctx context.Context, domain, selector string, privateKey any) HealthResult {
	res := h.CheckDKIM(ctx, domain, selector)
	if !res.Found || res.Error != "" {
		return res
	}

	want, err := dkimPublicKeyBase64(privateKey)
	if err != nil {
		return HealthResult{
			Found:   res.Found,
			Record:  res.Record,
			Error:   err.Error(),
			Details: "could not derive the public key from the private key supplied",
		}
	}

	got := dkimPublishedKey(res.Record)
	switch {
	case got == "":
		res.Valid = false
		res.Details = "DKIM record has no p= tag, so nothing can verify a signature"
	case subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1:
		res.Valid = true
		res.Details = "published key matches the signing key"
	default:
		res.Valid = false
		res.Details = "published key does NOT match the signing key: " +
			"messages signed with this key will fail DKIM verification"
	}
	return res
}

// CheckDKIMKey verifies that the published DKIM record matches a signing key.
func CheckDKIMKey(ctx context.Context, domain, selector string, privateKey any) HealthResult {
	return HealthChecker{}.CheckDKIMKey(ctx, domain, selector, privateKey)
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

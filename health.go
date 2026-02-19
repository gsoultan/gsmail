package gsmail

import (
	"context"
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

// CheckDomainHealth performs comprehensive DNS health checks for the given domain.
func CheckDomainHealth(ctx context.Context, domain string, selectors []string) (DomainHealth, error) {
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
		mxs, err := lookupMX(ctx, domain)
		res := HealthResult{}
		if err != nil {
			res.Error = err.Error()
		} else if len(mxs) > 0 {
			res.Found = true
			res.Valid = true
			var mxList []string
			for _, mx := range mxs {
				mxList = append(mxList, fmt.Sprintf("%s (%d)", mx.Host, mx.Pref))
			}
			res.Record = strings.Join(mxList, ", ")
		} else {
			res.Details = "No MX records found"
		}
		select {
		case resChan <- result{typ: "mx", res: res}:
		case <-ctx.Done():
		}
	}()

	// Check SPF
	wg.Add(1)
	go func() {
		defer wg.Done()
		res := CheckSPF(ctx, domain)
		select {
		case resChan <- result{typ: "spf", res: res}:
		case <-ctx.Done():
		}
	}()

	// Check DMARC
	wg.Add(1)
	go func() {
		defer wg.Done()
		res := CheckDMARC(ctx, domain)
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
			res := CheckDKIM(ctx, domain, s)
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

// CheckSPF retrieves and validates the SPF record for a domain.
func CheckSPF(ctx context.Context, domain string) HealthResult {
	txts, err := lookupTXT(ctx, domain)
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
func CheckDMARC(ctx context.Context, domain string) HealthResult {
	dmarcDomain := "_dmarc." + domain
	txts, err := lookupTXT(ctx, dmarcDomain)
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
func CheckDKIM(ctx context.Context, domain, selector string) HealthResult {
	if selector == "" {
		return HealthResult{Error: "Selector is required for DKIM check"}
	}

	dkimDomain := selector + "._domainkey." + domain
	txts, err := lookupTXT(ctx, dkimDomain)
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

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	dnsErr, ok := err.(*net.DNSError)
	return ok && dnsErr.IsNotFound
}

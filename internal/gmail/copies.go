package gmail

import (
	"fmt"
	"net/mail"
	"strings"
)

// Copies names who gets a message besides its To (pi-vi issue #878). Before it
// existed, an agent asked to "cc me" could only send the email and then mail a
// separate copy, so the other side's answers never reached the person who
// asked to be copied.
type Copies struct {
	Cc  []*mail.Address
	Bcc []*mail.Address
	// ReplyAll applies to a reply only: everyone the answered message went to
	// (its To and Cc) is added to Cc, less this account, the reply's own
	// recipient and anyone already on Bcc.
	ReplyAll bool
}

// ParseRecipients reads a comma-separated address list ("a@x.com, Name
// <b@y.com>"), as a Cc or Bcc argument arrives. Empty is no addresses. Anything
// net/mail cannot parse is refused rather than passed into a header: a value
// carrying CR or LF would otherwise write headers of its own.
func ParseRecipients(list string) ([]*mail.Address, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	if strings.ContainsAny(list, "\r\n") {
		return nil, fmt.Errorf("address list may not contain line breaks")
	}
	addresses, err := mail.ParseAddressList(list)
	if err != nil {
		return nil, fmt.Errorf("parse addresses %q: %w", list, err)
	}
	return addresses, nil
}

// lenientAddresses reads an address header from a received message. Mail from
// the wild does not always parse as a whole, so a list that fails is retried
// entry by entry and what parses is kept. A quoted display name with a comma in
// it is the one thing the per-entry retry gets wrong; it costs that entry only.
func lenientAddresses(header string) []*mail.Address {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	if addresses, err := mail.ParseAddressList(header); err == nil {
		return addresses
	}
	var found []*mail.Address
	for _, part := range strings.Split(header, ",") {
		if address, err := mail.ParseAddress(strings.TrimSpace(part)); err == nil {
			found = append(found, address)
		}
	}
	return found
}

// FormatRecipients renders addresses as a header value, display names encoded
// as RFC 2047 where needed. Entries are folded onto continuation lines so a long
// Cc stays inside RFC 5322's line limit.
func FormatRecipients(addresses []*mail.Address) string {
	parts := make([]string, len(addresses))
	for i, address := range addresses {
		parts[i] = address.String()
	}
	return strings.Join(parts, ",\r\n ")
}

// copyHeaders renders the Cc and Bcc header lines, CRLF-terminated, for
// buildMessage's extraHeaders. Gmail's messages.send delivers to Bcc and strips
// the header from what every recipient receives.
func copyHeaders(cc, bcc []*mail.Address) string {
	var b strings.Builder
	if len(cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", FormatRecipients(cc))
	}
	if len(bcc) > 0 {
		fmt.Fprintf(&b, "Bcc: %s\r\n", FormatRecipients(bcc))
	}
	return b.String()
}

// mergeRecipients appends add to base, keeping the first of any address seen
// twice and dropping every address in exclude. Addresses compare
// case-insensitively.
func mergeRecipients(base, add []*mail.Address, exclude ...string) []*mail.Address {
	seen := map[string]bool{}
	for _, address := range exclude {
		if address != "" {
			seen[strings.ToLower(address)] = true
		}
	}
	var merged []*mail.Address
	for _, address := range append(append([]*mail.Address{}, base...), add...) {
		key := strings.ToLower(address.Address)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, address)
	}
	return merged
}

// addressesOf lists the bare addresses of a list, for mergeRecipients' exclude.
func addressesOf(addresses []*mail.Address) []string {
	out := make([]string, len(addresses))
	for i, address := range addresses {
		out[i] = address.Address
	}
	return out
}

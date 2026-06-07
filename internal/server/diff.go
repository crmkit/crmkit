package server

import (
	"fmt"
	"strings"

	"github.com/crmkit/crmkit/internal/protocol"
)

// changeDetail formats a list of field changes as a compact, grepable audit
// detail, e.g. "stage: lead -> customer; owner: (none) -> alice". Only fields
// that actually changed appear; the result is "" when nothing meaningful did, so
// an update that touches only ignored fields records an empty detail.
func changeDetail(changes ...[3]string) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		name, before, after := c[0], c[1], c[2]
		if before == after {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", name, orNone(before), orNone(after)))
	}
	return strings.Join(parts, "; ")
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// orName prefers the resolved relation name, falling back to its handle.
func orName(name, handle string) string {
	if name != "" {
		return name
	}
	return handle
}

func money2(cents int64) string {
	if cents == 0 {
		return ""
	}
	return fmt.Sprintf("%.2f", float64(cents)/100)
}

// diffContact summarises the meaningful field changes between two contact states
// for the audit detail. Long/low-signal fields (notes, custom, follow-up) are
// intentionally omitted to keep history scannable.
func diffContact(before, after protocol.Contact) string {
	return changeDetail(
		[3]string{"name", before.Name, after.Name},
		[3]string{"email", before.Email, after.Email},
		[3]string{"phone", before.Phone, after.Phone},
		[3]string{"stage", before.Stage, after.Stage},
		[3]string{"owner", before.Owner, after.Owner},
		[3]string{"company", orName(before.CompanyName, before.CompanyHandle), orName(after.CompanyName, after.CompanyHandle)},
		[3]string{"tags", strings.Join(before.Tags, ","), strings.Join(after.Tags, ",")},
	)
}

func diffCompany(before, after protocol.Company) string {
	return changeDetail(
		[3]string{"name", before.Name, after.Name},
		[3]string{"domain", before.Domain, after.Domain},
		[3]string{"tags", strings.Join(before.Tags, ","), strings.Join(after.Tags, ",")},
	)
}

func diffDeal(before, after protocol.Deal) string {
	return changeDetail(
		[3]string{"title", before.Title, after.Title},
		[3]string{"stage", before.Stage, after.Stage},
		[3]string{"status", before.Status, after.Status},
		[3]string{"amount", money2(before.AmountCents), money2(after.AmountCents)},
		[3]string{"contact", orName(before.ContactName, before.ContactHandle), orName(after.ContactName, after.ContactHandle)},
		[3]string{"company", orName(before.CompanyName, before.CompanyHandle), orName(after.CompanyName, after.CompanyHandle)},
	)
}

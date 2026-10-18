package dto

import "strings"

// MaskEmail keeps the first character of the local part and the domain: "alice@example.com" → "a***@example.com".
// Values without an "@" collapse to "***"; an empty value stays empty.
func MaskEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "***"
	}
	local, domain := email[:at], email[at+1:]
	if local == "" {
		return "***@" + domain
	}
	first := []rune(local)[0]
	return string(first) + "***@" + domain
}

// RedactAdminUsageLogForVendor strips end-user data a vendor workspace must not see from an admin usage row.
//
// Users, API keys and subscriptions are station-owner data. The vendor keeps only what it needs to tell rows apart:
// the user id with a masked email, and the key id with its name. The key plaintext, balances, notification settings and
// the subscription body are dropped (the ids on the row itself are kept). The response shape is unchanged, so the Web
// usage table keeps working for vendors.
func RedactAdminUsageLogForVendor(l *AdminUsageLog) {
	if l == nil {
		return
	}
	if u := l.User; u != nil {
		l.User = &User{ID: u.ID, Email: MaskEmail(u.Email), DeletedAt: u.DeletedAt}
	}
	if k := l.APIKey; k != nil {
		l.APIKey = &APIKey{ID: k.ID, UserID: k.UserID, Name: k.Name}
	}
	l.Subscription = nil
}

package dto

import "strings"

const (
	apiKeyMaskPrefixRunes = 3
	apiKeyMaskSuffixRunes = 4
	deletedAPIKeyPrefix   = "__deleted__"
)

// MaskAPIKey keeps the first 3 and last 4 characters ("sk-…3f9a"). Deleted
// keys are stored as a "__deleted__" tombstone and mask to "".
func MaskAPIKey(key string) string {
	if key == "" || strings.HasPrefix(key, deletedAPIKeyPrefix) {
		return ""
	}
	runes := []rune(key)
	if len(runes) <= apiKeyMaskPrefixRunes+apiKeyMaskSuffixRunes {
		return "…"
	}
	return string(runes[:apiKeyMaskPrefixRunes]) + "…" + string(runes[len(runes)-apiKeyMaskSuffixRunes:])
}

// HideFullKey blanks the plaintext key, keeping key_masked (key_view=masked).
func (k *APIKey) HideFullKey() {
	if k != nil {
		k.Key = ""
	}
}

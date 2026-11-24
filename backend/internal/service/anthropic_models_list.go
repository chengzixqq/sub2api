package service

import (
	"slices"
	"strings"
)

// ModelCatalog carries the request's available IDs and, for Anthropic, their
// authorized accounts. Nil ModelIDs selects defaults. An empty slice is authoritative.
type ModelCatalog struct {
	ModelIDs []string
	accounts []Account
}

// AnthropicLongContextVariants returns additional public IDs in source order.
// The source can include platform defaults when the group's listing allows them.
func (c ModelCatalog) AnthropicLongContextVariants(group *Group, models []string) []string {
	if len(models) == 0 || len(c.accounts) == 0 {
		return nil
	}
	metadata := make([]*UpstreamModelMetadataSnapshot, len(c.accounts))
	for i := range c.accounts {
		metadata[i] = c.accounts[i].GetUpstreamModelMetadataSnapshot()
	}

	seen := make(map[string]bool, len(models))
	for _, model := range models {
		seen[strings.ToLower(model)] = true
	}
	var variants []string
	for _, model := range models {
		key := strings.ToLower(model)
		variantKey := key + claudeCodeLongContextModelSuffix
		if seen[variantKey] || strings.HasSuffix(key, claudeCodeLongContextModelSuffix) || strings.Contains(model, "*") {
			continue
		}
		var routedAccountIDs []int64
		if group != nil {
			routedAccountIDs = group.GetRoutingAccountIDs(model)
		}
		for i, snapshot := range metadata {
			account := &c.accounts[i]
			if snapshot == nil || (len(routedAccountIDs) > 0 && !slices.Contains(routedAccountIDs, account.ID)) || !account.IsModelSupported(model) {
				continue
			}
			target := strings.TrimSpace(account.GetMappedModel(model))
			if snapshot.Models[target].ContextWindow >= 900_000 {
				variants = append(variants, model+claudeCodeLongContextModelSuffix)
				seen[variantKey] = true
				break
			}
		}
	}
	return variants
}

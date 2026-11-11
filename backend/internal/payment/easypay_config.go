package payment

import (
	"fmt"
	"strings"
)

// NormalizeEasyPayRefundConfig accepts the upstream alias while keeping the
// established storage key. Omitted fields stay omitted for PATCH semantics;
// empty values retain the provider's default success code of 1.
func NormalizeEasyPayRefundConfig(config map[string]string) (map[string]string, error) {
	canonical, hasCanonical := config["v1RefundSuccessCode"]
	alias, hasAlias := config["refundSuccessCode"]
	canonical, alias = strings.TrimSpace(canonical), strings.TrimSpace(alias)
	for key, value := range map[string]string{"v1RefundSuccessCode": canonical, "refundSuccessCode": alias} {
		if value != "" && value != "0" && value != "1" {
			return nil, fmt.Errorf("easypay %s must be 0 or 1", key)
		}
	}
	if canonical != "" && alias != "" && canonical != alias {
		return nil, fmt.Errorf("easypay v1RefundSuccessCode and refundSuccessCode conflict")
	}
	result := make(map[string]string, len(config))
	for key, value := range config {
		result[key] = value
	}
	delete(result, "refundSuccessCode")
	if hasCanonical || hasAlias {
		if canonical == "" {
			canonical = alias
		}
		result["v1RefundSuccessCode"] = canonical
	}
	return result, nil
}

package payment

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEasyPayRefundConfig_Aliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  map[string]string
		want    string
		present bool
		invalid bool
	}{
		{name: "omitted", config: map[string]string{}},
		{name: "legacy", config: map[string]string{"v1RefundSuccessCode": "0"}, want: "0", present: true},
		{name: "alias", config: map[string]string{"refundSuccessCode": "0"}, want: "0", present: true},
		{name: "matching", config: map[string]string{"v1RefundSuccessCode": " 0 ", "refundSuccessCode": "0"}, want: "0", present: true},
		{name: "empty canonical", config: map[string]string{"v1RefundSuccessCode": "", "refundSuccessCode": "0"}, want: "0", present: true},
		{name: "empty alias", config: map[string]string{"v1RefundSuccessCode": "0", "refundSuccessCode": ""}, want: "0", present: true},
		{name: "empty both", config: map[string]string{"v1RefundSuccessCode": "", "refundSuccessCode": ""}, present: true},
		{name: "conflict", config: map[string]string{"v1RefundSuccessCode": "1", "refundSuccessCode": "0"}, invalid: true},
		{name: "invalid canonical", config: map[string]string{"v1RefundSuccessCode": "2"}, invalid: true},
		{name: "invalid alias", config: map[string]string{"refundSuccessCode": "auto"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := maps.Clone(tc.config)
			got, err := NormalizeEasyPayRefundConfig(tc.config)
			require.Equal(t, before, tc.config, "normalization must not mutate caller input")
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			value, present := got["v1RefundSuccessCode"]
			require.Equal(t, tc.want, value)
			require.Equal(t, tc.present, present)
			require.NotContains(t, got, "refundSuccessCode")
		})
	}
}

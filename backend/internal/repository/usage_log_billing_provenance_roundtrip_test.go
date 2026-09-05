package repository

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件锁定 usage_logs.billing_provenance 的 NULL 语义边界,是失败计费特性的
// 「正向锚点」:失败行必须带 provenance(已由 handler 层 sink 测试覆盖),而
// 成功行必须落 NULL,否则 Dashboard 的成功过滤器(usageLogSuccessFilter)会把
// 历史成功行一起排除,统计口径当场破裂。
//
// 生产代码里 BillingProvenance 只有两个赋值点,都在失败 sink 内
// (handler/billing_settlement_guard.go、handler/openai_billing_failure_sink.go),
// 成功路径从不设置该字段。这里钉住的是「不设置」到「落 NULL」之间那段
// 由 nullString 承担的转换,以及读回时不把 NULL 复原成空串指针。

func TestBillingProvenanceNullSemantics(t *testing.T) {
	estimated := "estimated"
	failedUpstream := "failed_upstream"
	failedEstimated := "failed_estimated"
	empty := ""

	tests := []struct {
		name      string
		in        *string
		wantValid bool
		wantValue string
		why       string
	}{
		{
			name:      "成功路径未设置则落 NULL",
			in:        nil,
			wantValid: false,
			why:       "成功请求必须落 NULL,与历史行一致;落成空串会让成功过滤器误判",
		},
		{
			name:      "空串同样落 NULL 而不是空字符串行",
			in:        &empty,
			wantValid: false,
			why:       "空串是「没有来源」的等价表达,不能落成一个既非 NULL 又非合法枚举值的行",
		},
		{
			name:      "estimated 原样落库",
			in:        &estimated,
			wantValid: true,
			wantValue: "estimated",
			why:       "成功但用量为本地估算,必须可区分于上游真实 usage",
		},
		{
			name:      "failed_upstream 原样落库",
			in:        &failedUpstream,
			wantValid: true,
			wantValue: "failed_upstream",
			why:       "失败但拿到上游真实 usage,是最该计费的一类",
		},
		{
			name:      "failed_estimated 原样落库",
			in:        &failedEstimated,
			wantValid: true,
			wantValue: "failed_estimated",
			why:       "失败且用量为本地估算,金额来自估算器",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nullString(tc.in)
			require.Equal(t, tc.wantValid, got.Valid, tc.why)
			if tc.wantValid {
				assert.Equal(t, tc.wantValue, got.String, tc.why)
			}
		})
	}
}

// TestBillingProvenanceScanRoundTrip 覆盖读回方向:NULL 必须复原成 nil 指针,
// 而不是指向空串的指针——后者会让上层「是否失败计费行」的判断从 nil 检查
// 变成需要额外比空串,任何漏比的调用点都会把成功行当成失败行。
//
// 这里复刻 usage_log_repo_query.go:669-671 的条件赋值形态,该处是全仓
// billing_provenance 唯一的 scan 出口。
func TestBillingProvenanceScanRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		scanned sql.NullString
		wantNil bool
		want    string
	}{
		{
			name:    "NULL 复原为 nil 指针",
			scanned: sql.NullString{},
			wantNil: true,
		},
		{
			name:    "failed_estimated 复原为对应值",
			scanned: sql.NullString{String: "failed_estimated", Valid: true},
			want:    "failed_estimated",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out *string
			if tc.scanned.Valid {
				out = &tc.scanned.String
			}

			if tc.wantNil {
				require.Nil(t, out, "NULL 必须复原成 nil,不能是指向空串的指针")
				return
			}
			require.NotNil(t, out)
			assert.Equal(t, tc.want, *out)
		})
	}
}

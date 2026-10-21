package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Push events are built from rows the dispatcher scans. Titles and bodies carry IDs, platform, severity and the
// error category only: no account names, user data or raw upstream error text, because the text passes through
// Apple / Bark / ntfy / webhook servers. Clients open the item by ID and read the details from the admin API.

const (
	pushTitleMaxRunes = 120
	pushBodyMaxRunes  = 240
	// pushAccountErrorBucket throttles account.error: one push per account per 30-minute bucket of error_at.
	pushAccountErrorBucket = 30 * time.Minute
	pushModelMaxRunes      = 64
)

var pushErrorKindLabels = map[string][2]string{
	AccountErrorKindAuth:          {"认证失败", "authentication failed"},
	AccountErrorKindForbidden:     {"访问被拒绝", "access forbidden"},
	AccountErrorKindQuota:         {"额度耗尽", "quota exhausted"},
	AccountErrorKindCustomCode:    {"自定义错误码", "custom error code"},
	AccountErrorKindStreamTimeout: {"流式超时", "stream timeout"},
	AccountErrorKindTokenRefresh:  {"令牌刷新失败", "token refresh failed"},
	AccountErrorKindPrivacy:       {"隐私设置未完成", "privacy not set"},
	AccountErrorKindRateLimit:     {"限流", "rate limited"},
	AccountErrorKindOther:         {"其他错误", "other error"},
}

func pushErrorKindLabel(kind string, statusCode *int) (string, string) {
	labels, ok := pushErrorKindLabels[kind]
	if !ok {
		labels = pushErrorKindLabels[AccountErrorKindOther]
	}
	if statusCode != nil {
		suffix := " (" + strconv.Itoa(*statusCode) + ")"
		return labels[0] + suffix, labels[1] + suffix
	}
	return labels[0], labels[1]
}

func pushContent(titleZh, bodyZh, titleEn, bodyEn, thread, level string, data map[string]any) PushContent {
	return PushContent{
		TitleZh: sanitizePushText(titleZh, pushTitleMaxRunes), BodyZh: sanitizePushText(bodyZh, pushBodyMaxRunes),
		TitleEn: sanitizePushText(titleEn, pushTitleMaxRunes), BodyEn: sanitizePushText(bodyEn, pushBodyMaxRunes),
		ThreadID: thread, InterruptionLevel: level, Data: data,
	}
}

func joinPushParts(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

func formatPushNumber(v float64) string {
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// pushAlertName is the rule name from the D06 snapshot (or current rule), falling back to the event title.
func pushAlertName(row *PushAlertEventRow) string {
	if name := strings.TrimSpace(row.RuleName); name != "" {
		return name
	}
	if title := strings.TrimSpace(row.Title); title != "" {
		return title
	}
	return "#" + strconv.FormatInt(row.RuleID, 10)
}

func pushAlertScope(row *PushAlertEventRow) (string, string) {
	var zh, en []string
	if row.Platform != "" {
		zh, en = append(zh, row.Platform), append(en, row.Platform)
	}
	if row.GroupID != nil {
		zh = append(zh, "分组 #"+strconv.FormatInt(*row.GroupID, 10))
		en = append(en, "group #"+strconv.FormatInt(*row.GroupID, 10))
	}
	return joinPushParts(zh...), joinPushParts(en...)
}

func pushAlertGroups(row *PushAlertEventRow) []int64 {
	if row.GroupID == nil {
		return nil
	}
	return []int64{*row.GroupID}
}

func pushAlertData(row *PushAlertEventRow) map[string]any {
	return map[string]any{"alert_event_id": row.EventID, "rule_id": row.RuleID}
}

func newAlertFiredPushEvent(row *PushAlertEventRow) PushEvent {
	severity := strings.ToUpper(strings.TrimSpace(row.Severity))
	title := "[" + severity + "] " + pushAlertName(row)
	var metricZh, metricEn string
	if row.MetricValue != nil {
		metricZh = "当前 " + formatPushNumber(*row.MetricValue)
		metricEn = "current " + formatPushNumber(*row.MetricValue)
		if row.ThresholdValue != nil {
			threshold := strings.TrimSpace(row.Operator + " " + formatPushNumber(*row.ThresholdValue))
			metricZh += "（阈值 " + threshold + "）"
			metricEn += " (threshold " + threshold + ")"
		}
	}
	var windowZh, windowEn string
	if row.WindowMinutes > 0 {
		windowZh = fmt.Sprintf("近 %d 分钟", row.WindowMinutes)
		windowEn = fmt.Sprintf("last %d min", row.WindowMinutes)
	}
	scopeZh, scopeEn := pushAlertScope(row)
	level := pushInterruptionActive
	if pushSeverityRank(severity) <= 1 {
		level = pushInterruptionUrgent
	}
	return PushEvent{
		Type:       PushEventOpsAlertFired,
		DedupKey:   PushEventOpsAlertFired + ":" + strconv.FormatInt(row.EventID, 10),
		OccurredAt: row.FiredAt,
		Severity:   severity,
		Platform:   row.Platform,
		GroupIDs:   pushAlertGroups(row),
		Content: pushContent(title, joinPushParts(metricZh, windowZh, scopeZh), title, joinPushParts(metricEn, windowEn, scopeEn),
			"ops-alert", level, pushAlertData(row)),
	}
}

func newAlertResolvedPushEvent(row *PushAlertEventRow) PushEvent {
	severity := strings.ToUpper(strings.TrimSpace(row.Severity))
	resolvedAt := row.FiredAt
	if row.ResolvedAt != nil {
		resolvedAt = *row.ResolvedAt
	}
	minutes := int(math.Ceil(resolvedAt.Sub(row.FiredAt).Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	howZh, howEn := "", ""
	if row.Status == OpsAlertStatusManualResolved {
		howZh, howEn = "已手动解决", "resolved manually"
	}
	scopeZh, scopeEn := pushAlertScope(row)
	name := pushAlertName(row)
	return PushEvent{
		Type:       PushEventOpsAlertResolved,
		DedupKey:   PushEventOpsAlertResolved + ":" + strconv.FormatInt(row.EventID, 10),
		OccurredAt: resolvedAt,
		Severity:   severity,
		Platform:   row.Platform,
		GroupIDs:   pushAlertGroups(row),
		Content: pushContent(
			"["+severity+"] 已恢复："+name, joinPushParts(howZh, fmt.Sprintf("持续 %d 分钟", minutes), scopeZh),
			"["+severity+"] Resolved: "+name, joinPushParts(howEn, fmt.Sprintf("lasted %d min", minutes), scopeEn),
			"ops-alert", pushInterruptionActive, pushAlertData(row)),
	}
}

func newAccountErrorPushEvent(row *PushAccountErrorRow) PushEvent {
	desc := DescribeAccountError(row.ErrorMessage)
	kind := desc.Kind
	if kind == "" {
		kind = AccountErrorKindOther
	}
	kindZh, kindEn := pushErrorKindLabel(kind, desc.StatusCode)
	id := strconv.FormatInt(row.AccountID, 10)
	bucket := row.ErrorAt.UTC().Unix() / int64(pushAccountErrorBucket/time.Second)
	return PushEvent{
		Type:       PushEventAccountError,
		DedupKey:   PushEventAccountError + ":" + id + ":" + strconv.FormatInt(bucket, 10),
		OccurredAt: row.ErrorAt,
		Platform:   row.Platform,
		GroupIDs:   row.GroupIDs,
		Content: pushContent("账号异常 #"+id, joinPushParts(row.Platform, kindZh), "Account error #"+id, joinPushParts(row.Platform, kindEn),
			"account", pushInterruptionActive, map[string]any{"account_id": row.AccountID, "error_kind": kind}),
	}
}

func newAccountTestFinishedPushEvent(row *PushTestRunRow) PushEvent {
	id := strconv.FormatInt(row.AccountID, 10)
	model := sanitizePushText(row.ModelID, pushModelMaxRunes)
	var titleZh, titleEn, bodyZh, bodyEn string
	switch row.Status {
	case AccountTestRunStatusSuccess:
		titleZh, titleEn = "账号测试成功 #"+id, "Account test passed #"+id
		latency := ""
		if row.LatencyMs != nil {
			latency = strconv.FormatInt(*row.LatencyMs, 10) + " ms"
		}
		bodyZh = joinPushParts(row.Platform, model, latency)
		bodyEn = bodyZh
	case AccountTestRunStatusFailed:
		titleZh, titleEn = "账号测试失败 #"+id, "Account test failed #"+id
		kind := AccountErrorKindOther
		if k := AccountTestErrorKind(row.Status, row.UpstreamStatusCode, row.ErrorMessage); k != nil {
			kind = *k
		}
		kindZh, kindEn := pushErrorKindLabel(kind, row.UpstreamStatusCode)
		bodyZh, bodyEn = joinPushParts(row.Platform, model, kindZh), joinPushParts(row.Platform, model, kindEn)
	default:
		titleZh, titleEn = "账号测试中断 #"+id, "Account test interrupted #"+id
		bodyZh, bodyEn = joinPushParts(row.Platform, model), joinPushParts(row.Platform, model)
	}
	return PushEvent{
		Type:            PushEventAccountTestFinished,
		DedupKey:        PushEventAccountTestFinished + ":" + row.RunID,
		OccurredAt:      row.FinishedAt,
		Platform:        row.Platform,
		GroupIDs:        row.GroupIDs,
		RecipientUserID: row.CreatedBy,
		Content: pushContent(titleZh, bodyZh, titleEn, bodyEn, "account-test", pushInterruptionActive,
			map[string]any{"account_id": row.AccountID, "run_id": row.RunID, "status": row.Status}),
	}
}

func newPushTestEvent(now time.Time) PushEvent {
	return PushEvent{
		Type:       PushEventTest,
		DedupKey:   PushEventTest + ":" + uuid.NewString(),
		OccurredAt: now,
		Content: pushContent("Sub2 推送测试", "这台设备可以收到 Sub2 的通知", "Sub2 push test", "This device can receive Sub2 notifications",
			"push-test", pushInterruptionActive, nil),
	}
}

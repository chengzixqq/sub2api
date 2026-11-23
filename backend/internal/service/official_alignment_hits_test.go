//go:build unit

package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type memoryHitStore struct {
	values    map[string]int64
	writes    int
	panicNext bool
}

type offlineHitStore struct{ reads, writes int }

func (s *offlineHitStore) IncrBy(context.Context, map[string]int64, time.Duration) error {
	s.writes++
	return errors.New("redis offline")
}
func (s *offlineHitStore) Read(context.Context, []string) ([]int64, error) {
	s.reads++
	return nil, errors.New("redis offline")
}

func TestAlignmentHitStorageFailurePreservesGatewayBytes(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		store := &offlineHitStore{}
		r := NewAlignmentHitRecorder(store)
		var wantBody, wantResponse []byte
		for _, recorder := range []*AlignmentHitRecorder{nil, r} {
			up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(200, cacheControlLimitOKMessage)}}
			s := fallbackNativeGatewayForTest(t, up, nil)
			s.settingService.alignmentHits = recorder
			s.settingService.alignmentSnapshot(t.Context())
			resp := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(resp)
			c.Request = httptest.NewRequest("POST", path, nil)
			c.Request.Header.Set("Anthropic-Beta", "client-unknown")
			p, err := ParseGatewayRequest(NewRequestBodyRef([]byte(cacheControlFiveClientBreakpoints)), PlatformAnthropic)
			require.NoError(t, err)
			a := fallbackNativeAccountForTest(false)
			if path == "/v1/messages" {
				_, err = s.Forward(c.Request.Context(), c, a, p)
			} else {
				err = s.ForwardCountTokens(c.Request.Context(), c, a, p)
			}
			require.NoError(t, err)
			require.Equal(t, 200, resp.Code)
			require.Len(t, up.bodies, 1)
			if recorder == nil {
				wantBody, wantResponse = up.bodies[0], resp.Body.Bytes()
			} else {
				require.Equal(t, wantBody, up.bodies[0])
				require.Equal(t, wantResponse, resp.Body.Bytes())
			}
		}
		require.Zero(t, store.reads)
		require.Zero(t, store.writes)
		r.Stop()
		require.Equal(t, 1, store.writes)
		_, err := r.Summary(t.Context(), nil)
		require.Error(t, err)
	}
}

func TestAlignmentHitAccountGateAndRetry(t *testing.T) {
	const history = `{"messages":[{"role":"assistant","content":[{"cache_control":{},"type":"redacted_thinking"}]}]}`
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(history)), PlatformAnthropic)
	require.NoError(t, err)
	s := NewSettingService(&betaReadSettings{}, nil)
	r := NewAlignmentHitRecorder(&memoryHitStore{})
	s.alignmentHits = r
	s.alignmentSnapshot(t.Context())
	for _, field := range alignmentHitFields {
		c := alignmentTestContext("/v1/messages")
		a := fallbackNativeAccountForTest(false)
		policy := DefaultClaudeCustomizationSettings()
		c.Request.Header.Set("Anthropic-Beta", "custom-miss")
		check := func() error {
			switch field {
			case ClaudeFieldUnknownBetaHeader:
				return s.checkUnknownBeta(t.Context(), c, a, policy)
			case ClaudeFieldThinkingCacheControl:
				return r.checkCacheControls(c, a, policy.ThinkingCacheControl, policy.CacheControlLimitMode, parsed)
			case ClaudeFieldCacheControlLimitMode:
				overflow, err := ParseGatewayRequest(NewRequestBodyRef([]byte(cacheControlFiveClientBreakpoints)), PlatformAnthropic)
				require.NoError(t, err)
				return r.checkCacheControls(c, a, policy.ThinkingCacheControl, policy.CacheControlLimitMode, overflow)
			default:
				_, _, err := r.applyContextManagementMissingBeta(c, a, policy, c.Request.Header, []byte(`{"context_management":{}}`))
				return err
			}
		}
		a.Type = AccountTypeBedrock
		require.NoError(t, check())
		a.Type = AccountTypeAPIKey
		a.ID = 7
		require.NoError(t, check())
		a.ID = 8
		require.NoError(t, check())
		for _, scope := range []int64{905, 7, 8} {
			got, err := r.Summary(t.Context(), &scope)
			require.NoError(t, err)
			var want int64
			if scope == 7 {
				want = 1
			}
			require.Equal(t, want, got[field].Last24h, field)
		}
	}
}

func TestAlignmentThinkingSkipsUnrelatedEscapes(t *testing.T) {
	for _, escape := range []string{`\u001b`, `\u00e9`, `\u4e2d`, `\\u0061`} {
		body := []byte(`[{"type":"thinking","thinking":"` + escape + `"},{"type":"text","cache_control":{}}]`)
		require.False(t, mayHaveThinkingCacheControl(body), escape)
	}
}

func TestAlignmentHitHandlesMissingURL(t *testing.T) {
	c := alignmentTestContext("/v1/messages")
	c.Request.URL = nil
	r := NewAlignmentHitRecorder(&memoryHitStore{})
	r.Record(c, ClaudeFieldUnknownBetaHeader, 7, ClaudeAlignPassthrough)
	got, err := r.Summary(t.Context(), nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, got[ClaudeFieldUnknownBetaHeader].Last24h)
}

func TestAlignmentHitLogLimit(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	synctest.Test(t, func(t *testing.T) {
		r := NewAlignmentHitRecorder(&memoryHitStore{values: map[string]int64{}})
		r.Start()
		for range 15 {
			r.Record(alignmentTestContext("/v1/messages"), ClaudeFieldUnknownBetaHeader, 7, "passthrough")
		}
		r.Record(alignmentTestContext("/v1/messages"), ClaudeFieldThinkingCacheControl, 8, "strip")
		synctest.Wait()
		require.Equal(t, 10, strings.Count(logs.String(), "alignment_switch=unknown_beta_header"))
		require.Equal(t, 1, strings.Count(logs.String(), "alignment_switch=thinking_cache_control"))
		time.Sleep(time.Second)
		r.Record(alignmentTestContext("/v1/messages"), ClaudeFieldUnknownBetaHeader, 7, "passthrough")
		r.Stop()
		require.Contains(t, logs.String(), "account_id=7 mode=passthrough request_path=/v1/messages suppressed=5")
		got, err := r.Summary(t.Context(), nil)
		require.NoError(t, err)
		require.EqualValues(t, 16, got[ClaudeFieldUnknownBetaHeader].Last24h)
	})
}

func TestAlignmentHitConcurrentFlush(t *testing.T) {
	r := NewAlignmentHitRecorder(&memoryHitStore{values: map[string]int64{}})
	var writers sync.WaitGroup
	for id := range int64(8) {
		writers.Go(func() {
			for range 100 {
				r.Record(alignmentTestContext("/v1/messages"), ClaudeFieldUnknownBetaHeader, id, "reject")
			}
		})
	}
	for range 25 {
		r.flush()
	}
	writers.Wait()
	r.flush()
	got, err := r.Summary(t.Context(), nil)
	require.NoError(t, err)
	require.EqualValues(t, 800, got[ClaudeFieldUnknownBetaHeader].Last24h)
}

func TestAlignmentThinkingPrefilterPreservesRejectPosition(t *testing.T) {
	for _, block := range []string{
		`{"type":"thinking","cache_control":{}}`,
		`{"type":"redacted_thinking","cache_control":{}}`,
		`{"cache_control":{}, "type" : "thinking"}`,
		`{"type":"th\u0069nking","cache_control":{}}`,
		`{"type":"thinking","cach\u0065_control":{}}`,
	} {
		body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"old } { block","nested":{"type":"thinking"}},{"type":"text","cache_control":{}},` + block + `]}]}`)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, err)
		for _, raw := range [][]byte{nil, parsed.MessagesRaw()} {
			i, j, _, ok := rejectableThinkingCacheControlPath(body, raw, http.Header{})
			require.True(t, ok, block)
			require.Equal(t, 0, i)
			require.Equal(t, 2, j)
		}
	}
}

func (m *memoryHitStore) IncrBy(_ context.Context, batch map[string]int64, ttl time.Duration) error {
	if m.panicNext {
		m.panicNext = false
		panic("flush failed")
	}
	m.writes++
	for k, n := range batch {
		m.values[k] += n
	}
	return nil
}

func (m *memoryHitStore) Read(_ context.Context, keys []string) ([]int64, error) {
	out := make([]int64, len(keys))
	for i, k := range keys {
		out[i] = m.values[k]
	}
	return out, nil
}

func alignmentTestContext(path string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", path, nil)
	return c
}

func TestAlignmentHitsFlushAndWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryHitStore{values: map[string]int64{}}
		r := NewAlignmentHitRecorder(store)
		r.Start()
		id := int64(7)
		field := ClaudeFieldUnknownBetaHeader
		now := time.Now().UTC()
		for _, age := range []int{0, 23, 24, 167, 168} {
			hour := now.Add(-time.Duration(age) * time.Hour).Format("2006010215")
			for _, scope := range []string{"all", "7"} {
				store.values["official_align:hits:"+field+":"+scope+":"+hour] = 1
			}
		}
		c := alignmentTestContext("/v1/messages")
		r.Record(c, field, id, "reject")
		r.Record(c, field, 8, "passthrough")
		for _, scope := range []*int64{nil, &id} {
			got, err := r.Summary(t.Context(), scope)
			require.NoError(t, err)
			require.Len(t, got, 4)
			require.Equal(t, AlignmentHitCounts{Last24h: 3, Last7d: 5}, got[field])
		}
		time.Sleep(9 * time.Second)
		synctest.Wait()
		require.Zero(t, store.writes)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Equal(t, 1, store.writes)
		require.EqualValues(t, 2, store.values["official_align:hits:"+field+":7:"+now.Format("2006010215")])
		store.panicNext = true
		r.Record(alignmentTestContext("/v1/messages"), field, id, "reject")
		time.Sleep(10 * time.Second)
		synctest.Wait()
		r.Record(alignmentTestContext("/v1/messages"), field, id, "reject")
		r.Stop()
		require.Equal(t, 2, store.writes)
		got, err := r.Summary(t.Context(), nil)
		require.NoError(t, err)
		require.Equal(t, AlignmentHitCounts{Last24h: 4, Last7d: 6}, got[field])
	})
}

func TestAlignmentHitsForwardMatchesReject(t *testing.T) {
	const plain = `{"model":"claude-opus-4-6","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct{ field, body, beta string }{
		{ClaudeFieldThinkingCacheControl, `{"model":"claude-opus-4-6","max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"old","cache_control":{}}]},{"role":"user","content":"hi"}]}`, ""},
		{ClaudeFieldCacheControlLimitMode, cacheControlFiveClientBreakpoints, ""},
		{ClaudeFieldUnknownBetaHeader, plain, "unregistered"},
		{ClaudeFieldContextManagementMissingBeta, `{"context_management":{},` + plain[1:], ""},
	} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			for _, scenario := range []string{"default", "reject", "no match", "after dispatch", "other protocol"} {
				t.Run(tc.field+path+"/"+scenario, func(t *testing.T) {
					up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(200, cacheControlLimitOKMessage)}}
					s := fallbackNativeGatewayForTest(t, up, nil)
					r := NewAlignmentHitRecorder(&memoryHitStore{values: map[string]int64{}})
					s.settingService.alignmentHits = r
					s.settingService.alignmentSnapshot(t.Context())
					a := fallbackNativeAccountForTest(false)
					c := alignmentTestContext(path)
					body, beta := tc.body, tc.beta
					if scenario != "default" {
						a.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{tc.field: ClaudeAlignReject}
					}
					switch scenario {
					case "no match":
						body, beta = plain, ""
					case "after dispatch":
						InstallOpsUpstreamAttemptClock(c)
						MarkOpsUpstreamAttemptStart(c.Request.Context(), a.ID)
					case "other protocol":
						c.Request.URL.Path = "/v1/chat/completions"
					}
					c.Request.Header.Set("Anthropic-Beta", beta)
					p, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
					require.NoError(t, err)
					if path == "/v1/messages" {
						_, err = s.Forward(c.Request.Context(), c, a, p)
					} else {
						err = s.ForwardCountTokens(c.Request.Context(), c, a, p)
					}
					if scenario == "reject" {
						require.ErrorIs(t, err, ErrLocalAnthropicRejected)
						require.Equal(t, 400, c.Writer.Status())
						require.Empty(t, up.bodies)
					} else {
						require.NoError(t, err)
					}
					got, err := r.Summary(t.Context(), &a.ID)
					require.NoError(t, err)
					var want int64
					if scenario == "default" || scenario == "reject" {
						want = 1
					}
					require.Equal(t, AlignmentHitCounts{Last24h: want, Last7d: want}, got[tc.field])
				})
			}
		}
	}
}

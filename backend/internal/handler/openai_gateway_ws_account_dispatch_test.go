package handler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesWebSocket_AccountGrantRevokedBeforeNextTurn(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			var revoked atomic.Bool
			keyRepo := newWSGroupPricingAPIKeyRepoStub(1)
			runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload: wsGroupPricingFirst, secondPayload: wsGroupPricingSecond,
				ingressMode:   mode,
				apiKeyService: newWSGroupPricingAPIKeyService(keyRepo), apiKeyCredential: wsGroupPricingKey,
				dispatchPolicyReader: func(context.Context, int64, int64) (*service.AccountDispatchPolicy, error) {
					policy := &service.AccountDispatchPolicy{Mode: service.AccountDispatchModeReplace}
					if !revoked.Load() {
						policy.Accounts = []service.AccountDispatchGrant{{AccountID: 9901, Origin: service.AccountDispatchOriginExtra}}
					}
					return policy, nil
				},
				afterFirstUpstreamRequest: func(*service.ChannelService) error { revoked.Store(true); return nil },
				secondTurnCloseExpected:   true,
				closeStatus:               coderws.StatusPolicyViolation,
				closeReason:               "account is no longer authorized for this connection",
			})
		})
	}
}

func TestOpenAIResponsesWebSocket_ExtraAccountKeepsRequestGroupBilling(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			accountRate := 0.2
			keyRepo := newWSGroupPricingAPIKeyRepoStub(3)
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload:  wsGroupPricingFirst,
				ingressMode:   mode,
				apiKeyService: newWSGroupPricingAPIKeyService(keyRepo), apiKeyCredential: wsGroupPricingKey,
				accountRateMultiplier: &accountRate,
				accountDispatchScope:  service.DispatchScopeAuthorizedOnly,
				dispatchPolicyReader: func(_ context.Context, userID, groupID int64) (*service.AccountDispatchPolicy, error) {
					if userID != 1701 || groupID != wsGroupPricingGroupID {
						return nil, fmt.Errorf("unexpected policy identity: user=%d group=%d", userID, groupID)
					}
					return &service.AccountDispatchPolicy{
						Mode:     service.AccountDispatchModeReplace,
						Accounts: []service.AccountDispatchGrant{{AccountID: 9901, Origin: service.AccountDispatchOriginExtra}},
					}, nil
				},
			})
			require.NotNil(t, got.log)
			require.Equal(t, &keyRepo.group.ID, got.log.GroupID)
			require.InDelta(t, 3.0, got.log.RateMultiplier, 1e-12)
			require.EqualValues(t, 9901, got.log.AccountID)
			require.Equal(t, &accountRate, got.log.AccountRateMultiplier)
			require.Greater(t, got.log.ActualCost, 0.0)
			require.Greater(t, got.log.AccountCost(), 0.0)
		})
	}
}

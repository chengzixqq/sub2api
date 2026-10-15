package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// Called once after a successful user forward, outside usage recording: billing
// retries and asynchronous settlement must not increment the request counter.
func (h *GatewayHandler) recordAccountRPM(ctx context.Context, account *service.Account) {
	if h.gatewayService == nil || account == nil {
		return
	}
	if err := h.gatewayService.IncrementAccountRPM(ctx, account.ID); err != nil {
		logger.FromContext(ctx).Warn("gateway.rpm_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}
}

// WebSocket callbacks run once per turn; only a completed successful turn counts.
// Non-WS forward errors (including retryable attempts and partial usage) do not.
func (h *OpenAIGatewayHandler) recordAccountRPM(ctx context.Context, account *service.Account, result *service.OpenAIForwardResult, forwardErr error) {
	if h.gatewayService == nil || account == nil || result == nil || forwardErr != nil || !result.SucceededForScheduling() {
		return
	}
	if result.OpenAIWSMode && result.UpstreamTerminalEvent != "response.completed" && result.UpstreamTerminalEvent != "response.done" {
		return
	}
	h.recordSuccessfulAccountRPM(ctx, account)
}

// Live session creation has its own result type. Count the accepted call once;
// sideband reconnects do not create another upstream inference session.
func (h *OpenAIGatewayHandler) recordSuccessfulAccountRPM(ctx context.Context, account *service.Account) {
	if h.gatewayService == nil || account == nil {
		return
	}
	if err := h.gatewayService.IncrementAccountRPM(ctx, account.ID); err != nil {
		logger.FromContext(ctx).Warn("gateway.rpm_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}
}

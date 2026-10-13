package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const refundIntentAction = "REFUND_INITIATED"
const refundGatewaySuccessAction = "REFUND_GATEWAY_SUCCEEDED"

// The intent is immutable. The order CAS and this required audit insertion are
// committed before sending money. Neither a timeout nor a restart can create a
// second refund number or change the amount/deduction policy of this operation.
type managedRefundIntent struct {
	RefundID        string  `json:"refundID"`
	ProviderID      string  `json:"providerID"`
	TradeNo         string  `json:"tradeNo"`
	OutTradeNo      string  `json:"outTradeNo"`
	Currency        string  `json:"currency"`
	GatewayAmount   string  `json:"gatewayAmount"`
	RefundAmount    float64 `json:"refundAmount"`
	Reason          string  `json:"reason"`
	Force           bool    `json:"force"`
	Deduct          bool    `json:"deduct"`
	DeductionType   string  `json:"deductionType"`
	BalanceToDeduct float64 `json:"balanceToDeduct"`
	SubDaysToDeduct int     `json:"subDaysToDeduct"`
	SubscriptionID  int64   `json:"subscriptionID"`
}

func isManagedRefundOrder(order *dbent.PaymentOrder) bool {
	snapshot := psOrderProviderSnapshot(order)
	return snapshot != nil && snapshot.ProviderKey == payment.TypeEasyPay && snapshot.APIVersion == "v2"
}

func (i *managedRefundIntent) matchesRequest(amount float64, force, deduct bool) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || (amount > 0 && math.Abs(amount-i.RefundAmount) > 0.000001) || force != i.Force || deduct != i.Deduct {
		return infraerrors.Conflict("REFUND_INTENT_LOCKED", "refund amount and deduction policy are fixed; query the existing refund")
	}
	return nil
}

func (i *managedRefundIntent) plan(order *dbent.PaymentOrder) *RefundPlan {
	amount, _ := strconv.ParseFloat(i.GatewayAmount, 64)
	return &RefundPlan{OrderID: order.ID, Order: order, RefundID: i.RefundID, ManagedRefund: true,
		RefundAmount: i.RefundAmount, GatewayAmount: amount, Reason: i.Reason, Force: i.Force,
		DeductBalance: i.Deduct, DeductionType: i.DeductionType, BalanceToDeduct: i.BalanceToDeduct,
		SubDaysToDeduct: i.SubDaysToDeduct, SubscriptionID: i.SubscriptionID}
}

func (s *PaymentService) loadManagedRefundIntent(ctx context.Context, client *dbent.Client, oid int64) (*managedRefundIntent, error) {
	entry, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ(refundIntentAction)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load refund intent: %w", err)
	}
	var intent managedRefundIntent
	if err := json.Unmarshal([]byte(entry.Detail), &intent); err != nil {
		return nil, fmt.Errorf("decode refund intent: %w", err)
	}
	amount, err := strconv.ParseFloat(intent.GatewayAmount, 64)
	if intent.RefundID == "" || intent.TradeNo == "" || intent.ProviderID == "" || err != nil || amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return nil, fmt.Errorf("refund intent for order %d is invalid", oid)
	}
	return &intent, nil
}

func writeRequiredRefundAudit(ctx context.Context, client *dbent.Client, oid int64, action string, detail any) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(oid, 10)).SetAction(action).SetOperator("admin").SetDetail(string(encoded)).Save(ctx)
	return err
}

func (s *PaymentService) executeManagedRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	order, err := s.entClient.PaymentOrder.Get(ctx, p.OrderID)
	if err != nil {
		return nil, err
	}
	intent, err := s.loadManagedRefundIntent(ctx, s.entClient, p.OrderID)
	if err != nil {
		return nil, err
	}
	if intent != nil {
		if err := intent.matchesRequest(p.RefundAmount, p.Force, p.DeductBalance); err != nil {
			return nil, err
		}
		return s.queryManagedRefund(ctx, order, intent)
	}
	if !isManagedRefundOrder(order) || strings.TrimSpace(order.PaymentTradeNo) == "" {
		return nil, infraerrors.BadRequest("INVALID_REFUND_BINDING", "V2 refund requires its original protocol snapshot and gateway trade number")
	}
	if math.IsNaN(p.RefundAmount) || math.IsInf(p.RefundAmount, 0) || p.RefundAmount <= 0 || p.RefundAmount-order.Amount > paymentAmountToleranceForCurrency(PaymentOrderCurrency(order)) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund amount")
	}
	p.GatewayAmount = calculateGatewayRefundAmount(order.Amount, order.PayAmount, p.RefundAmount, PaymentOrderCurrency(order))
	gatewayAmount := formatGatewayRefundAmount(p.GatewayAmount, order)
	parsedAmount, err := strconv.ParseFloat(gatewayAmount, 64)
	if err != nil || parsedAmount <= 0 || math.IsNaN(parsedAmount) || math.IsInf(parsedAmount, 0) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "refund amount is below the gateway currency minimum")
	}
	// Recheck eligibility immediately before persisting/sending; final deduction
	// still performs an atomic balance update or locks the subscription row.
	p.Order = order
	if p.DeductBalance {
		if result := s.prepDeduct(ctx, order, p, p.Force); result != nil {
			return result, nil
		}
		if p.DeductionType == payment.DeductionTypeSubscription && p.SubscriptionID == 0 {
			p.SubDaysToDeduct = 0
		}
	}
	digest := sha256.Sum256([]byte(psStringValue(order.ProviderInstanceID) + "\x00" + order.OutTradeNo + "\x00" + strconv.FormatInt(order.ID, 10)))
	intent = &managedRefundIntent{RefundID: fmt.Sprintf("s2r_%x", digest[:14]), ProviderID: psStringValue(order.ProviderInstanceID),
		TradeNo: order.PaymentTradeNo, OutTradeNo: order.OutTradeNo, Currency: PaymentOrderCurrency(order),
		GatewayAmount: gatewayAmount, RefundAmount: p.RefundAmount,
		Reason: p.Reason, Force: p.Force, Deduct: p.DeductBalance, DeductionType: p.DeductionType,
		BalanceToDeduct: p.BalanceToDeduct, SubDaysToDeduct: p.SubDaysToDeduct, SubscriptionID: p.SubscriptionID}
	prov, err := s.beginManagedRefund(ctx, p, intent)
	if err != nil {
		return nil, err
	}
	// Only the transaction winner reaches the POST. All later callers query the
	// durable intent, including a crash between commit and this outbound call.
	resp, gatewayErr := prov.Refund(ctx, payment.RefundRequest{RefundID: intent.RefundID, TradeNo: intent.TradeNo,
		OrderID: intent.OutTradeNo, Amount: intent.GatewayAmount, Reason: intent.Reason})
	return s.acceptManagedRefundResult(ctx, order, intent, resp, gatewayErr)
}

func (s *PaymentService) beginManagedRefund(ctx context.Context, p *RefundPlan, intent *managedRefundIntent) (payment.Provider, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	instanceID, err := strconv.ParseInt(intent.ProviderID, 10, 64)
	if err != nil {
		return nil, err
	}
	if err := lockPaymentProviderInstance(ctx, tx.Client(), instanceID); err != nil {
		return nil, err
	}
	// Pin credentials while holding the same lock used by config update/delete;
	// once this transaction commits REFUND_PENDING protects them until resolved.
	inst, err := tx.PaymentProviderInstance.Get(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if !inst.RefundEnabled {
		return nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not enabled for this provider")
	}
	configService := s.configService
	if configService == nil {
		configService = &PaymentConfigService{}
	}
	config, err := configService.decryptConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	version, err := payment.NormalizeEasyPayAPIVersion(config["apiVersion"])
	if err != nil || version != payment.EasyPayAPIVersionV2 || inst.ProviderKey != payment.TypeEasyPay {
		return nil, infraerrors.Conflict("REFUND_INTENT_MISMATCH", "original EasyPay V2 provider configuration changed")
	}
	if config == nil {
		config = map[string]string{}
	}
	config["paymentMode"] = inst.PaymentMode
	prov, err := createPaymentProviderFromInstance(inst.ProviderKey, intent.ProviderID, config)
	if err != nil {
		return nil, err
	}
	if err := validateProviderSnapshotMetadata(p.Order, prov.ProviderKey(), providerMerchantIdentityMetadata(prov)); err != nil {
		return nil, err
	}
	count, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundRequested)).
		SetStatus(OrderStatusRefundPending).SetRefundAmount(intent.RefundAmount).SetRefundReason(intent.Reason).SetForceRefund(intent.Force).Save(ctx)
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, infraerrors.Conflict("CONFLICT", "refund already initiated or order status changed")
	}
	if err := writeRequiredRefundAudit(ctx, tx.Client(), p.OrderID, refundIntentAction, intent); err != nil {
		return nil, fmt.Errorf("persist refund intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return prov, nil
}

func (s *PaymentService) managedRefundSucceeded(ctx context.Context, client *dbent.Client, oid int64) (bool, error) {
	return client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ(refundGatewaySuccessAction)).Exist(ctx)
}

func (s *PaymentService) queryManagedRefund(ctx context.Context, order *dbent.PaymentOrder, intent *managedRefundIntent) (*RefundResult, error) {
	if intent.ProviderID != psStringValue(order.ProviderInstanceID) || intent.TradeNo != order.PaymentTradeNo || intent.OutTradeNo != order.OutTradeNo || intent.Currency != PaymentOrderCurrency(order) {
		return nil, infraerrors.Conflict("REFUND_INTENT_MISMATCH", "order no longer matches its persisted refund identity")
	}
	if order.Status == OrderStatusRefunded || order.Status == OrderStatusPartiallyRefunded {
		return &RefundResult{Success: true}, nil
	}
	confirmed, err := s.managedRefundSucceeded(ctx, s.entClient, order.ID)
	if err != nil {
		return nil, err
	}
	if confirmed {
		return s.finalizeManagedRefund(ctx, order, intent)
	}
	prov, err := s.getRefundProvider(ctx, order)
	if err != nil {
		return nil, err
	}
	if err := validateProviderSnapshotMetadata(order, prov.ProviderKey(), providerMerchantIdentityMetadata(prov)); err != nil {
		return nil, err
	}
	query, ok := prov.(payment.RefundQueryProvider)
	if !ok {
		return nil, infraerrors.BadRequest("REFUND_QUERY_UNSUPPORTED", "provider cannot query the persisted refund")
	}
	resp, queryErr := query.QueryRefund(ctx, payment.RefundQueryRequest{RefundID: intent.RefundID, TradeNo: intent.TradeNo, OrderID: intent.OutTradeNo, Amount: intent.GatewayAmount})
	return s.acceptManagedRefundResult(ctx, order, intent, resp, queryErr)
}

func (s *PaymentService) acceptManagedRefundResult(ctx context.Context, order *dbent.PaymentOrder, intent *managedRefundIntent, resp *payment.RefundResponse, gatewayErr error) (*RefundResult, error) {
	// Errors can occur after the gateway has sent money; never treat an error or
	// an unverifiable/mismatched response as permission to submit a new refund.
	if gatewayErr != nil || resp == nil || resp.RefundID != intent.RefundID {
		return &RefundResult{Warning: "gateway refund outcome is unknown; query the existing refund before further action"}, nil
	}
	status := strings.TrimSpace(resp.Status)
	if status != payment.ProviderStatusSuccess && status != payment.ProviderStatusRefunded && status != payment.ProviderStatusFailed {
		return &RefundResult{Warning: "gateway refund is pending confirmation"}, nil
	}
	confirmed, err := s.recordManagedRefundOutcome(ctx, order.ID, intent, status)
	if err != nil {
		return nil, err
	}
	if confirmed {
		return s.finalizeManagedRefund(ctx, order, intent)
	}
	return &RefundResult{Warning: "gateway reports this refund failed; the original refund identity is retained"}, nil
}

func (s *PaymentService) recordManagedRefundOutcome(ctx context.Context, oid int64, intent *managedRefundIntent, status string) (bool, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// This update serializes concurrent responses/finalizers on the order row.
	count, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.StatusIn(OrderStatusRefundPending, OrderStatusRefunding, OrderStatusRefundFailed)).SetStatus(OrderStatusRefundPending).Save(ctx)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, infraerrors.Conflict("CONFLICT", "refund state changed")
	}
	confirmed, err := s.managedRefundSucceeded(ctx, tx.Client(), oid)
	if err != nil {
		return false, err
	}
	if !confirmed && (status == payment.ProviderStatusSuccess || status == payment.ProviderStatusRefunded) {
		if err := writeRequiredRefundAudit(ctx, tx.Client(), oid, refundGatewaySuccessAction, map[string]string{"refundID": intent.RefundID, "gatewayAmount": intent.GatewayAmount}); err != nil {
			return false, err
		}
		confirmed = true
	} else if !confirmed {
		if err := tx.PaymentOrder.UpdateOneID(oid).SetStatus(OrderStatusRefundFailed).Exec(ctx); err != nil {
			return false, err
		}
		logged, err := tx.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ("REFUND_FAILED")).Exist(ctx)
		if err != nil {
			return false, err
		}
		if !logged {
			if err := writeRequiredRefundAudit(ctx, tx.Client(), oid, "REFUND_FAILED", map[string]string{"refundID": intent.RefundID}); err != nil {
				return false, err
			}
		}
	}
	return confirmed, tx.Commit()
}

func (s *PaymentService) finalizeManagedRefund(ctx context.Context, order *dbent.PaymentOrder, intent *managedRefundIntent) (*RefundResult, error) {
	p := intent.plan(order)
	result, err := s.finalizePendingRefundSuccess(ctx, p)
	if err != nil {
		// Gateway success was persisted independently, so a local accounting
		// failure can be retried without another payment or even another query.
		return nil, fmt.Errorf("gateway refund succeeded; local settlement remains pending and recoverable: %w", err)
	}
	// Subscription helpers invalidate during an outer transaction as well;
	// invalidate again after commit so no pre-commit cache entry can survive.
	if p.DeductionType == payment.DeductionTypeSubscription && order.SubscriptionGroupID != nil && s.subscriptionSvc != nil {
		_ = s.subscriptionSvc.invalidateSubscriptionCaches(order.UserID, *order.SubscriptionGroupID)
	}
	return result, nil
}

//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type managedRefundProviderStub struct {
	refundProviderTestDouble
	posts   atomic.Int32
	queries atomic.Int32
	refund  func(payment.RefundRequest) (*payment.RefundResponse, error)
	query   func(payment.RefundQueryRequest) (*payment.RefundResponse, error)
}

func (p *managedRefundProviderStub) ProviderKey() string { return payment.TypeEasyPay }
func (p *managedRefundProviderStub) Refund(_ context.Context, request payment.RefundRequest) (*payment.RefundResponse, error) {
	p.posts.Add(1)
	return p.refund(request)
}
func (p *managedRefundProviderStub) QueryRefund(_ context.Context, request payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.queries.Add(1)
	return p.query(request)
}

type managedRefundLoadBalancer struct{ captureLoadBalancer }

func (*managedRefundLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return map[string]string{"apiVersion": "v2"}, nil
}

func newManagedRefundFixture(t *testing.T) (*PaymentService, *dbent.PaymentOrder, *managedRefundProviderStub) {
	return newManagedRefundFixtureWithClient(t, newPaymentConfigServiceTestClient(t))
}

func newManagedRefundFixtureWithClient(t *testing.T, client *dbent.Client) (*PaymentService, *dbent.PaymentOrder, *managedRefundProviderStub) {
	t.Helper()
	ctx := context.Background()
	_, indexErr := client.ExecContext(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_audit_logs_order_action_uniq ON payment_audit_logs(order_id, action)")
	require.NoError(t, indexErr)
	order := createPendingRefundOrderForTest(t, ctx, client, "managed-refund")
	id, err := strconv.ParseInt(*order.ProviderInstanceID, 10, 64)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.UpdateOneID(id).SetProviderKey(payment.TypeEasyPay).SetConfig(`{"apiVersion":"v2"}`).Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).SetProviderKey(payment.TypeEasyPay).
		SetPayAmount(106).SetProviderSnapshot(map[string]any{"schema_version": 2, "provider_key": payment.TypeEasyPay, "provider_instance_id": *order.ProviderInstanceID, "api_version": "v2"}).Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(order.UserID).SetBalance(100).Save(ctx)
	require.NoError(t, err)
	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID, Balance: 100}}
	userRepo.deductAvailableBalanceFn = func(ctx context.Context, id int64, amount float64) (float64, error) {
		tx := dbent.TxFromContext(ctx)
		require.NotNil(t, tx)
		user, err := tx.User.Get(ctx, id)
		if err != nil {
			return 0, err
		}
		deducted := math.Min(amount, math.Max(0, user.Balance))
		_, err = tx.User.UpdateOneID(id).AddBalance(-deducted).Save(ctx)
		return deducted, err
	}
	svc := &PaymentService{entClient: client, loadBalancer: &managedRefundLoadBalancer{}, userRepo: userRepo}
	prov := &managedRefundProviderStub{
		refund: func(req payment.RefundRequest) (*payment.RefundResponse, error) {
			return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusPending}, nil
		},
		query: func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
			return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess}, nil
		},
	}
	t.Cleanup(replacePaymentProviderFactoryForTest(t, prov))
	return svc, order, prov
}

func prepareManagedRefund(t *testing.T, svc *PaymentService, oid int64, deduct bool) *RefundPlan {
	t.Helper()
	plan, early, err := svc.PrepareRefund(context.Background(), oid, 40, "partial", false, deduct)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NotNil(t, plan)
	return plan
}

func TestManagedRefund_UnknownResponseRestartUsesFrozenAmountAndNoNewPost(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	var refundID string
	prov.refund = func(req payment.RefundRequest) (*payment.RefundResponse, error) {
		refundID = req.RefundID
		require.Len(t, refundID, 32)
		require.Equal(t, "42.40", req.Amount)
		return nil, errors.New("EOF after gateway accepted refund")
	}
	result, err := svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, true))
	require.NoError(t, err)
	require.False(t, result.Success)
	user, err := svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance, "unknown gateway result must retain user balance")
	// A new process, and subsequently edited order fee, must use the original
	// intent amount rather than local recharge amount or a recalculation.
	_, err = svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetPayAmount(200).Save(ctx)
	require.NoError(t, err)
	prov.query = func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
		require.Equal(t, refundID, req.RefundID)
		require.Equal(t, "42.40", req.Amount)
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess}, nil
	}
	restarted := &PaymentService{entClient: svc.entClient, loadBalancer: svc.loadBalancer, userRepo: svc.userRepo}
	result, err = restarted.ExecuteRefund(ctx, prepareManagedRefund(t, restarted, order.ID, true))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, int32(1), prov.posts.Load())
	require.Equal(t, int32(1), prov.queries.Load())
	user, err = svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 60.0, user.Balance)
	reloaded, err := svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPartiallyRefunded, reloaded.Status)
	result, err = restarted.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	user, err = svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 60.0, user.Balance)
}

func TestManagedRefund_FixedDeductionPolicyAndUndeductedSuccess(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, false))
	require.NoError(t, err)
	for _, request := range []struct {
		amount        float64
		force, deduct bool
	}{{41, false, false}, {40, true, false}, {40, false, true}, {math.NaN(), false, false}} {
		_, _, err = svc.PrepareRefund(ctx, order.ID, request.amount, "changed", request.force, request.deduct)
		require.Equal(t, "REFUND_INTENT_LOCKED", infraerrors.Reason(err))
	}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	user, err := svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance, "original deduct=false must survive query/finalization")
	require.Equal(t, int32(1), prov.posts.Load())
}

func TestManagedRefund_GatewaySuccessSurvivesLocalSettlementFailure(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	prov.refund = func(req payment.RefundRequest) (*payment.RefundResponse, error) {
		_, err := svc.entClient.User.UpdateOneID(order.UserID).SetBalance(10).Save(ctx)
		require.NoError(t, err)
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess}, nil
	}
	_, err := svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, true))
	require.ErrorContains(t, err, "local settlement remains pending")
	confirmed, err := svc.managedRefundSucceeded(ctx, svc.entClient, order.ID)
	require.NoError(t, err)
	require.True(t, confirmed)
	user, err := svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 10.0, user.Balance, "insufficient non-force deduction must roll back")
	reloaded, err := svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	_, err = svc.entClient.User.UpdateOneID(order.UserID).SetBalance(50).Save(ctx)
	require.NoError(t, err)
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, int32(1), prov.posts.Load())
	require.Zero(t, prov.queries.Load(), "durable gateway success must avoid another gateway request")
	user, err = svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 10.0, user.Balance)
}

func TestManagedRefund_ConcurrentCallerQueriesWhileFirstPostIsInFlight(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	firstPlan := prepareManagedRefund(t, svc, order.ID, false)
	stalePlan := prepareManagedRefund(t, svc, order.ID, false)
	entered, release := make(chan struct{}), make(chan struct{})
	prov.refund = func(req payment.RefundRequest) (*payment.RefundResponse, error) {
		close(entered)
		<-release
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusPending}, nil
	}
	prov.query = func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusPending}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := svc.ExecuteRefund(ctx, firstPlan); done <- err }()
	<-entered
	_, err := svc.ExecuteRefund(ctx, stalePlan)
	close(release)
	require.NoError(t, err)
	require.NoError(t, <-done)
	require.Equal(t, int32(1), prov.posts.Load())
	require.Equal(t, int32(1), prov.queries.Load())
	count, err := svc.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ(refundIntentAction)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestManagedRefund_IntentCommitBeforePostCrashNeverResubmits(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	plan := prepareManagedRefund(t, svc, order.ID, false)
	intent := &managedRefundIntent{RefundID: "stable-crash-refund", ProviderID: *order.ProviderInstanceID, TradeNo: order.PaymentTradeNo, OutTradeNo: order.OutTradeNo, Currency: PaymentOrderCurrency(order), GatewayAmount: "42.40", RefundAmount: 40, DeductionType: payment.DeductionTypeNone}
	_, beginErr := svc.beginManagedRefund(ctx, plan, intent)
	require.NoError(t, beginErr)
	prov.query = func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
		return nil, errors.New("gateway has not observed the request")
	}
	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Zero(t, prov.posts.Load())
	require.Equal(t, int32(1), prov.queries.Load())
}

func TestManagedRefund_RejectsMissingTradeNumberBeforePost(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	_, err := svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetPaymentTradeNo("").Save(ctx)
	require.NoError(t, err)
	_, err = svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, false))
	require.Equal(t, "INVALID_REFUND_BINDING", infraerrors.Reason(err))
	require.Zero(t, prov.posts.Load())
	intent, err := svc.loadManagedRefundIntent(ctx, svc.entClient, order.ID)
	require.NoError(t, err)
	require.Nil(t, intent)
}

func TestManagedRefund_RejectsSubCurrencyRefundBeforeIntent(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 0.001, "too small", false, false)
	require.NoError(t, err)
	require.Nil(t, early)
	_, err = svc.ExecuteRefund(ctx, plan)
	require.Equal(t, "INVALID_AMOUNT", infraerrors.Reason(err))
	intent, err := svc.loadManagedRefundIntent(ctx, svc.entClient, order.ID)
	require.NoError(t, err)
	require.Nil(t, intent)
	reloaded, err := svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Zero(t, prov.posts.Load())
}

func TestManagedRefund_RepeatedConfirmedFailureIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, order, prov := newManagedRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, true))
	require.NoError(t, err)
	prov.query = func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusFailed}, nil
	}
	for range 2 {
		result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
		require.NoError(t, err)
		require.False(t, result.Success)
	}
	require.Equal(t, int32(1), prov.posts.Load())
	count, err := svc.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_FAILED")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	user, err := svc.entClient.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance)
}

func TestManagedRefund_RequiredAuditFailuresCannotLoseMoneyOrIntent(t *testing.T) {
	for _, action := range []string{refundIntentAction, "REFUND_SUCCESS"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			svc, order, prov := newManagedRefundFixture(t)
			fail := true
			svc.entClient.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
				return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
					audit, ok := m.(*dbent.PaymentAuditLogMutation)
					if !ok {
						return nil, errors.New("unexpected audit mutation type")
					}
					actual, _ := audit.Action()
					if fail && actual == action {
						return nil, errors.New("injected required audit failure")
					}
					return next.Mutate(ctx, m)
				})
			})
			prov.refund = func(req payment.RefundRequest) (*payment.RefundResponse, error) {
				return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess}, nil
			}
			_, err := svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, true))
			require.ErrorContains(t, err, "injected required audit failure")
			user, err := svc.entClient.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Equal(t, 100.0, user.Balance, "failed audit must rollback accounting")
			intent, err := svc.loadManagedRefundIntent(ctx, svc.entClient, order.ID)
			require.NoError(t, err)
			if action == refundIntentAction {
				require.Nil(t, intent)
				require.Zero(t, prov.posts.Load())
			} else {
				require.NotNil(t, intent)
				require.Equal(t, int32(1), prov.posts.Load())
				confirmed, err := svc.managedRefundSucceeded(ctx, svc.entClient, order.ID)
				require.NoError(t, err)
				require.True(t, confirmed)
				fail = false
				result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
				require.NoError(t, err)
				require.True(t, result.Success)
				require.Equal(t, int32(1), prov.posts.Load())
				require.Zero(t, prov.queries.Load())
			}
		})
	}
}

type managedRefundSubscriptionRepo struct {
	transactionalBulkSubscriptionRepo
}

func (r *managedRefundSubscriptionRepo) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	copy := r.committed
	return &copy, nil
}

func (r *managedRefundSubscriptionRepo) Delete(ctx context.Context, id int64) error {
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}
	r.pending[dbent.TxFromContext(ctx)].Status = SubscriptionStatusRevoked
	return nil
}

func TestManagedRefund_SubscriptionShortenAndRevokeRollbackWithSettlement(t *testing.T) {
	for _, remainingDays := range []int{60, 20} {
		t.Run(strconv.Itoa(remainingDays), func(t *testing.T) {
			ctx := context.Background()
			svc, order, prov := newManagedRefundFixture(t)
			var err error
			order, err = svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).SetSubscriptionGroupID(88).SetSubscriptionDays(30).Save(ctx)
			require.NoError(t, err)
			expiresAt := time.Now().AddDate(0, 0, remainingDays)
			repo := &managedRefundSubscriptionRepo{transactionalBulkSubscriptionRepo: transactionalBulkSubscriptionRepo{
				committed: UserSubscription{ID: 77, UserID: order.UserID, GroupID: 88, Status: SubscriptionStatusActive, ExpiresAt: expiresAt},
				pending:   make(map[*dbent.Tx]*UserSubscription), reads: make(map[*dbent.Tx]int),
			}}
			svc.subscriptionSvc = NewSubscriptionService(nil, repo, nil, svc.entClient, nil)
			t.Cleanup(svc.subscriptionSvc.Stop)
			fail := true
			svc.entClient.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
				return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
					audit, ok := m.(*dbent.PaymentAuditLogMutation)
					if !ok {
						return nil, errors.New("unexpected audit mutation type")
					}
					action, _ := audit.Action()
					if fail && action == "REFUND_SUCCESS" {
						return nil, errors.New("injected subscription settlement audit failure")
					}
					return next.Mutate(ctx, m)
				})
			})
			prov.refund = func(req payment.RefundRequest) (*payment.RefundResponse, error) {
				return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess}, nil
			}
			_, err = svc.ExecuteRefund(ctx, prepareManagedRefund(t, svc, order.ID, true))
			require.ErrorContains(t, err, "injected subscription settlement audit failure")
			require.Equal(t, expiresAt, repo.committed.ExpiresAt)
			require.Equal(t, SubscriptionStatusActive, repo.committed.Status, "revocation must roll back with failed settlement")
			fail = false
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, 30, result.SubDaysDeducted)
			if remainingDays == 20 {
				require.Equal(t, SubscriptionStatusRevoked, repo.committed.Status)
			} else {
				require.Equal(t, expiresAt.AddDate(0, 0, -30), repo.committed.ExpiresAt)
			}
			require.Equal(t, int32(1), prov.posts.Load())
			require.Zero(t, prov.queries.Load())
		})
	}
}

//go:build unit

package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Opt-in real PostgreSQL checks use a fresh schema and never share payment rows
// with other tests. The schema is removed on completion.
func newPaymentPostgresTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	dsn := os.Getenv("PAYMENT_V2_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAYMENT_V2_TEST_DATABASE_URL for real transaction tests")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	databaseName := strings.TrimPrefix(u.Path, "/")
	require.True(t, (u.Scheme == "postgres" || u.Scheme == "postgresql") && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") && strings.HasPrefix(databaseName, "sub2api_") && strings.HasSuffix(databaseName, "_test"), "payment transaction tests require a loopback PostgreSQL URL and a sub2api_*_test database")
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("paymentv2_%d", time.Now().UnixNano())
	_, err = db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`) })
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	scopedDB, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, scopedDB)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(ctx))
	return client
}

func TestManagedRefund_PostgresConcurrentPostAndSettlement(t *testing.T) {
	client := newPaymentPostgresTestClient(t)
	svc, order, prov := newManagedRefundFixtureWithClient(t, client)
	ctx := context.Background()
	plans := make([]*RefundPlan, 8)
	for i := range plans {
		plans[i] = prepareManagedRefund(t, svc, order.ID, true)
	}
	prov.query = func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusPending}, nil
	}
	run := func(call func(int) error) {
		start := make(chan struct{})
		results := make(chan error, len(plans))
		var wg sync.WaitGroup
		for i := range plans {
			wg.Add(1)
			go func(i int) { defer wg.Done(); <-start; results <- call(i) }(i)
		}
		close(start)
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				require.Equal(t, "CONFLICT", infraerrors.Reason(err), "unexpected concurrent error: %v", err)
			}
		}
	}
	run(func(i int) error { _, err := svc.ExecuteRefund(ctx, plans[i]); return err })
	require.Equal(t, int32(1), prov.posts.Load(), "concurrent requests must submit exactly one external refund")
	prov.query = func(req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
		return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess}, nil
	}
	run(func(int) error { _, err := svc.QueryAndFinalizeRefund(ctx, order.ID); return err })
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 60.0, user.Balance, "concurrent success responses must deduct exactly once")
	count, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, int32(1), prov.posts.Load())
}

func TestEasyPayConfig_PostgresFirstOrderSerializesProtocolChange(t *testing.T) {
	client := newPaymentPostgresTestClient(t)
	ctx := context.Background()
	configSvc := &PaymentConfigService{entClient: client}
	inst, err := client.PaymentProviderInstance.Create().SetName("first-order-provider").SetProviderKey(payment.TypeEasyPay).SetConfig(`{"apiVersion":"v1"}`).SetSupportedTypes(payment.TypeAlipay).SetEnabled(true).Save(ctx)
	require.NoError(t, err)
	user, err := client.User.Create().SetEmail("first-order@example.com").SetUsername("first-order").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	client.PaymentOrder.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			if m.Op() == dbent.OpCreate {
				close(entered)
				<-release
			}
			return next.Mutate(ctx, m)
		})
	})
	svc := &PaymentService{entClient: client, configService: configSvc}
	created := make(chan error, 1)
	go func() {
		_, err := svc.createOrderInTx(ctx, CreateOrderRequest{UserID: user.ID, PaymentType: payment.TypeAlipay, OrderType: payment.OrderTypeBalance}, &User{ID: user.ID, Email: user.Email}, nil, &PaymentConfig{MaxPendingOrders: 3}, 10, 10, 0, 10, 0, &payment.InstanceSelection{InstanceID: strconv.FormatInt(inst.ID, 10), ProviderKey: payment.TypeEasyPay, Config: map[string]string{"apiVersion": "v1"}})
		created <- err
	}()
	<-entered
	updated := make(chan error, 1)
	go func() {
		_, err := configSvc.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: map[string]string{"apiVersion": "v2"}, Enabled: boolPtrValue(false)})
		updated <- err
	}()
	select {
	case err := <-updated:
		close(release)
		t.Fatalf("configuration crossed an in-flight first order: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-created)
	require.Equal(t, "PROVIDER_PROTOCOL_LOCKED", infraerrors.Reason(<-updated))
	current, err := client.PaymentProviderInstance.Get(ctx, inst.ID)
	require.NoError(t, err)
	require.Contains(t, current.Config, `"v1"`)
}

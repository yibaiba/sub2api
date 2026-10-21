//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestRefundRetryPreservesHeldBalance(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusFailed} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "retry-held-"+status,
				`{"refundID":"rf_test","deductionHeld":true,"deductionType":"balance","balanceDeducted":60}`)
			wallet := &refundHoldWallet{balance: 40}
			repo := wallet.repo(order.UserID)
			repo.updateBalanceFn = func(context.Context, int64, float64) error {
				return errors.New("temporary database failure")
			}
			prov := &pendingRefundProviderTestDouble{queryStatus: payment.ProviderStatusFailed}
			restore := replacePaymentProviderFactoryForTest(t, prov)
			defer restore()
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: repo}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.Contains(t, result.Warning, "rollback failed")

			repo.updateBalanceFn = func(_ context.Context, _ int64, amount float64) error {
				wallet.restoreCalls++
				wallet.balance += amount
				return nil
			}
			plan, early, err := svc.PrepareRefund(ctx, order.ID, 0, "retry", true, true)
			require.NoError(t, err)
			require.Nil(t, early)
			result, err = svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			require.Contains(t, result.Warning, "pending confirmation")
			require.Zero(t, wallet.deductCalls, "reuse the original deduction without charging again")
			detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, 60.0, detail.BalanceDeducted)

			prov.queryStatus = status
			result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			if status == payment.ProviderStatusSuccess {
				require.True(t, result.Success)
				require.Equal(t, 60.0, result.BalanceDeducted)
				require.Equal(t, 40.0, wallet.balance)
				return
			}
			require.False(t, result.Success)
			require.Equal(t, 100.0, wallet.balance, "failed retry returns the original held amount")
			require.Equal(t, 1, wallet.restoreCalls)
			// An older rollback failure must not bypass deduction after its hold was returned.
			repo.getByIDUser.Balance = wallet.balance
			plan, early, err = svc.PrepareRefund(ctx, order.ID, 0, "third attempt", true, true)
			require.NoError(t, err)
			require.Nil(t, early)
			_, err = svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			require.Equal(t, 1, wallet.deductCalls)
			require.Zero(t, wallet.balance)
		})
	}
}

func TestRestoreRefundHeldDeductionAuditHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actions []string
		details []string
		want    float64
		wantErr bool
	}{
		{name: "legacy failure", actions: []string{"REFUND_ROLLBACK_FAILED", "REFUND_FAILED"}, details: []string{`{"balanceDeducted":60}`, `{"detail":"gateway failed"}`}, want: 60},
		{name: "legacy hold returned", actions: []string{"REFUND_ROLLBACK_FAILED", "REFUND_PENDING", "REFUND_FAILED"}, details: []string{`{"balanceDeducted":60}`, `{}`, `{"detail":"gateway failed"}`}},
		{name: "direct rollback returned", actions: []string{"REFUND_ROLLBACK_FAILED", "REFUND_GATEWAY_FAILED"}, details: []string{`{"balanceDeducted":60}`, `{}`}},
		{name: "confirmed return", actions: []string{"REFUND_ROLLBACK_FAILED", "REFUND_FAILED"}, details: []string{`{"balanceDeducted":60}`, `{"deductionHeld":false,"balanceDeducted":60}`}},
		{name: "malformed audit", actions: []string{"REFUND_ROLLBACK_FAILED"}, details: []string{`invalid`}, wantErr: true},
		{name: "missing amount", actions: []string{"REFUND_ROLLBACK_FAILED"}, details: []string{`{}`}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			for i, action := range tc.actions {
				_, err := client.PaymentAuditLog.Create().SetOrderID("1").SetAction(action).
					SetDetail(tc.details[i]).SetOperator("admin").Save(ctx)
				require.NoError(t, err)
			}
			plan := &RefundPlan{OrderID: 1, DeductionType: payment.DeductionTypeNone}
			err := (&PaymentService{entClient: client}).restoreRefundHeldDeduction(ctx, plan)
			if tc.wantErr {
				require.Error(t, err)
				require.False(t, plan.deductionHeld)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want > 0, plan.deductionHeld)
			require.Equal(t, tc.want, plan.BalanceToDeduct)
		})
	}
}

type refundRetrySubscriptionRepo struct {
	*subscriptionUserSubRepoStub
	extendCalls int
}

func (r *refundRetrySubscriptionRepo) ExtendExpiry(_ context.Context, id int64, expiry time.Time) error {
	r.extendCalls++
	r.byID[id].ExpiresAt = expiry
	return nil
}

func TestRefundRetryPreservesHeldSubscription(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "retry-subscription",
		`{"deductionHeld":true,"deductionType":"subscription","subDaysDeducted":5,"subscriptionID":42}`)
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).
		SetOrderType(payment.OrderTypeSubscription).Save(ctx)
	require.NoError(t, err)
	// Older rollback-failure rows omit type/id; the original pending snapshot
	// must identify the held subscription, not a newly selected subscription.
	for _, action := range []string{"REFUND_ROLLBACK_FAILED", "REFUND_FAILED"} {
		detail := `{"subDaysDeducted":5}`
		if action == "REFUND_FAILED" {
			detail = `{"detail":"gateway failed"}`
		}
		_, err := client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(order.ID, 10)).SetAction(action).
			SetDetail(detail).SetOperator("admin").Save(ctx)
		require.NoError(t, err)
	}
	baseline := time.Now().AddDate(0, 0, 10)
	repo := &refundRetrySubscriptionRepo{subscriptionUserSubRepoStub: newSubscriptionUserSubRepoStub()}
	repo.seed(&UserSubscription{ID: 42, UserID: order.UserID, GroupID: 1, ExpiresAt: baseline, Status: SubscriptionStatusActive})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	t.Cleanup(subSvc.Stop)
	prov := &pendingRefundProviderTestDouble{queryStatus: payment.ProviderStatusFailed}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, subscriptionSvc: subSvc}
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 0, "retry", true, false)
	require.NoError(t, err)
	require.Nil(t, early)
	_, err = svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.Zero(t, repo.extendCalls, "do not deduct the held days again")
	detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, 5, detail.SubDaysDeducted)
	require.Equal(t, int64(42), detail.SubscriptionID)
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, 1, repo.extendCalls)
	require.True(t, baseline.AddDate(0, 0, 5).Equal(repo.byID[42].ExpiresAt))
}

func TestRefundRetryUnreadableAuditDoesNotCallProvider(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createCompletedRefundableOrderForTest(t, ctx, client, "retry-invalid-audit")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_ROLLBACK_FAILED").SetDetail("invalid").SetOperator("admin").Save(ctx)
	require.NoError(t, err)
	wallet := &refundHoldWallet{balance: 100}
	prov := &pendingRefundProviderTestDouble{}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, userRepo: wallet.repo(order.UserID)}
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 0, "retry", true, true)
	require.NoError(t, err)
	require.Nil(t, early)
	_, err = svc.ExecuteRefund(ctx, plan)
	require.ErrorContains(t, err, "parse refund deduction audit")
	require.Zero(t, wallet.deductCalls)
	require.Zero(t, prov.refundCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
}

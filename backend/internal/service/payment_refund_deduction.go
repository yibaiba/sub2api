package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// restoreRefundHeldDeduction carries an unreturned deduction into the next
// attempt. A historical rollback failure alone is not proof it is still held.
func (s *PaymentService) restoreRefundHeldDeduction(ctx context.Context, p *RefundPlan) error {
	entry, err := s.latestRefundDeductionEvent(ctx, strconv.FormatInt(p.OrderID, 10), 0)
	if err != nil || entry == nil || entry.Action == "REFUND_GATEWAY_FAILED" {
		return err
	}
	detail, err := s.refundHeldDeductionDetail(ctx, entry)
	if err != nil || detail == nil {
		return err
	}
	detail.applyTo(p)
	return nil
}

func (s *PaymentService) latestRefundDeductionEvent(ctx context.Context, orderID string, beforeID int64) (*dbent.PaymentAuditLog, error) {
	actions := []string{"REFUND_FAILED", "REFUND_ROLLBACK_FAILED", "REFUND_GATEWAY_FAILED"}
	if beforeID > 0 {
		actions = append(actions, "REFUND_PENDING")
	}
	query := s.entClient.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(orderID),
		paymentauditlog.ActionIn(actions...),
	)
	if beforeID > 0 {
		// A pending snapshot bounds a legacy failure: reaching it without a
		// rollback-failed event means that attempt returned its deduction.
		query = query.Where(paymentauditlog.IDLT(beforeID))
	}
	entry, err := query.Order(paymentauditlog.ByID(sql.OrderDesc())).First(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load refund deduction audit: %w", err)
	}
	return entry, nil
}

func (s *PaymentService) refundHeldDeductionDetail(ctx context.Context, entry *dbent.PaymentAuditLog) (*refundPendingAuditDetail, error) {
	var detail refundPendingAuditDetail
	var outcome struct {
		DeductionHeld *bool `json:"deductionHeld"`
	}
	if err := json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
		return nil, fmt.Errorf("parse refund deduction audit: %w", err)
	}
	if err := json.Unmarshal([]byte(entry.Detail), &outcome); err != nil {
		return nil, fmt.Errorf("parse refund deduction outcome: %w", err)
	}
	if entry.Action == "REFUND_FAILED" && outcome.DeductionHeld == nil {
		previous, err := s.latestRefundDeductionEvent(ctx, entry.OrderID, entry.ID)
		if err != nil || previous == nil || previous.Action != "REFUND_ROLLBACK_FAILED" {
			return nil, err
		}
		return s.refundHeldDeductionDetail(ctx, previous)
	}
	if outcome.DeductionHeld != nil && !*outcome.DeductionHeld {
		return nil, nil
	}
	return s.completeRefundHeldDeduction(ctx, entry, detail)
}

func (s *PaymentService) completeRefundHeldDeduction(ctx context.Context, entry *dbent.PaymentAuditLog, detail refundPendingAuditDetail) (*refundPendingAuditDetail, error) {
	// Legacy rollback audit rows contain amounts but not the deduction type.
	if detail.DeductionType == "" {
		switch {
		case detail.BalanceDeducted > 0:
			detail.DeductionType = payment.DeductionTypeBalance
		case detail.SubDaysDeducted > 0:
			detail.DeductionType = payment.DeductionTypeSubscription
		}
	}
	if detail.DeductionType == payment.DeductionTypeSubscription && detail.SubscriptionID == 0 {
		orderID, err := strconv.ParseInt(entry.OrderID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse refund audit order id: %w", err)
		}
		pending, err := s.latestRefundPendingDetail(ctx, orderID)
		if err != nil {
			return nil, err
		}
		detail.SubscriptionID = pending.SubscriptionID
		if detail.SubscriptionID == 0 {
			return nil, fmt.Errorf("refund rollback audit %d is missing the held subscription id", entry.ID)
		}
	}
	if (detail.DeductionType != payment.DeductionTypeBalance || detail.BalanceDeducted <= 0) &&
		(detail.DeductionType != payment.DeductionTypeSubscription || detail.SubDaysDeducted <= 0) {
		return nil, fmt.Errorf("refund rollback audit %d is missing its held deduction", entry.ID)
	}
	return &detail, nil
}

func refundDeductionAuditFields(p *RefundPlan, held bool) map[string]any {
	return map[string]any{
		"deductionHeld": held, "deductionType": p.DeductionType,
		"balanceDeducted": p.BalanceToDeduct, "subDaysDeducted": p.SubDaysToDeduct,
		"subscriptionID": p.SubscriptionID,
	}
}

type refundRollbackFailure struct {
	gatewayErr  error
	rollbackErr error
}

func (s *PaymentService) recordRefundRollbackFailure(ctx context.Context, p *RefundPlan, failure refundRollbackFailure) {
	detail := refundDeductionAuditFields(p, true)
	detail["gatewayError"] = psErrMsg(failure.gatewayErr)
	detail["rollbackError"] = psErrMsg(failure.rollbackErr)
	s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED", "admin", detail)
}

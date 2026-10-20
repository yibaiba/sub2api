//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiCachedContentsCreate_ChargesShorterUpstreamLifetime(t *testing.T) {
	for _, balance := range []float64{500, 6} {
		t.Run(fmt.Sprintf("balance=%g", balance), func(t *testing.T) {
			env := newCachedBillingEnv(t, balance, 900)
			expireTime := time.Now().Add(time.Hour)
			env.upstream.createBody = cachedBillingCreateBody(expireTime)

			rec := env.create()
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			totals := env.waitForUsage(t, 1)
			require.Len(t, totals, 1)
			want := cachedBillingTokens * (cachedBillingInputPrice + cachedBillingStoragePrice)
			require.InDelta(t, want, totals[0], 0.01, "申请 24 小时、实际 1 小时，只预留并收取 1 小时存储费")
			require.Len(t, env.repo.records, 1)
			for _, record := range env.repo.records {
				require.True(t, expireTime.Equal(record.ExpireTime), "保存的到期时间与计费口径一致")
			}
			require.Zero(t, env.upstream.methodCalls(http.MethodDelete))
		})
	}
}

func TestGeminiCachedContentsPatch_MalformedSuccessKeepsMutationWithoutCharge(t *testing.T) {
	for name, body := range map[string]string{
		"html":              "<html>upstream error</html>",
		"empty":             "",
		"null response":     `null`,
		"missing expiry":    `{}`,
		"null expiry":       `{"expireTime":null}`,
		"zero expiry":       `{"expireTime":"0001-01-01T00:00:00Z"}`,
		"invalid expiry":    `{"expireTime":"not-a-time"}`,
		"wrong expiry type": `{"expireTime":123}`,
		"truncated":         `{"expireTime":"2099-01-01T00:00:00Z","name":`,
		"trailing json":     `{"expireTime":"2099-01-01T00:00:00Z"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			env := newCachedBillingEnv(t, 500, 900)
			baseline := time.Now().Add(time.Hour)
			env.seed(baseline)
			env.upstream.failures = map[string]cachedBillingUpstreamFailure{
				http.MethodPatch: {status: http.StatusOK, body: body},
			}

			rec := env.patch()
			require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
			require.Empty(t, env.usage.totals(), "无法确认续期结果时不收费")
			require.True(t, baseline.Equal(env.repo.records["abc"].ExpireTime))
			require.True(t, env.repo.pendingMutation(100), "结果未知，保留意图以供回滚")
			require.Equal(t, 1, env.upstream.methodCalls(http.MethodPatch))
			require.Equal(t, http.StatusConflict, env.patch().Code, "恢复前不能覆盖未知的修改")
			require.Equal(t, 1, env.upstream.methodCalls(http.MethodPatch))
		})
	}
}

func TestGeminiCachedContentMutationRecovery_MalformedRollbackKeepsIntent(t *testing.T) {
	env := newCachedBillingEnv(t, 500, 900)
	baseline := time.Now().Add(time.Hour)
	env.seed(baseline)
	env.upstream.failures = map[string]cachedBillingUpstreamFailure{
		http.MethodPatch: {status: http.StatusOK, body: `<html>upstream error</html>`},
	}
	require.Equal(t, http.StatusBadGateway, env.patch().Code)
	env.repo.expireLease(100)

	recovered, err := env.h.geminiCachedContentService.RecoverStaleMutations(context.Background())
	var malformed *service.GeminiCachedContentMalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Zero(t, recovered)
	require.True(t, env.repo.pendingMutation(100))
	require.True(t, baseline.Equal(env.repo.records["abc"].ExpireTime))
	require.Empty(t, env.usage.totals())

	delete(env.upstream.failures, http.MethodPatch)
	env.repo.expireLease(100)
	recovered, err = env.h.geminiCachedContentService.RecoverStaleMutations(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.False(t, env.repo.pendingMutation(100))
	requireTTLNear(t, env.upstream.patchTTLs[2], time.Until(baseline))
	require.Empty(t, env.usage.totals())
}

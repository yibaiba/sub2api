//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeminiCachedContentPatchUpstream_ValidExpiry(t *testing.T) {
	const expiry = "2026-10-16T12:00:00.123456789+08:00"
	svc, upstream := newGeminiCachedContentServiceForTest(t, http.StatusOK,
		`{"expireTime":"`+expiry+`"}`)
	account := &Account{ID: 9, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
	record := &GeminiCachedContent{UpstreamName: "cachedContents/up1"}

	result, err := svc.PatchUpstream(context.Background(), account, record, time.Hour)
	require.NoError(t, err)
	require.Equal(t, record.UpstreamName, result.Name)
	want, err := time.Parse(time.RFC3339Nano, expiry)
	require.NoError(t, err)
	require.True(t, want.Equal(result.ExpireTime))
	require.Equal(t, http.MethodPatch, upstream.lastReq.Method)
	require.Equal(t, "ttl", upstream.lastReq.URL.Query().Get("updateMask"))
}

func TestGeminiCachedContentPatchUpstream_ResponseSizeBoundary(t *testing.T) {
	const validResponse = `{"expireTime":"2099-01-01T00:00:00Z"}`
	// A valid JSON prefix at the read limit must not hide an unread suffix.
	atLimit := validResponse + strings.Repeat(" ", geminiCachedContentResponseMaxBytes-len(validResponse))
	for _, tc := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "at limit", body: atLimit},
		{name: "over limit", body: atLimit + " ", wantError: true},
		{name: "invalid suffix beyond limit", body: atLimit + "<html>", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newGeminiCachedContentServiceForTest(t, http.StatusOK, tc.body)
			account := &Account{ID: 9, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
			result, err := svc.PatchUpstream(context.Background(), account,
				&GeminiCachedContent{UpstreamName: "cachedContents/up1"}, time.Hour)
			if tc.wantError {
				var malformed *GeminiCachedContentMalformedResponseError
				require.ErrorAs(t, err, &malformed)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.False(t, result.ExpireTime.IsZero())
		})
	}
}

func TestGeminiCachedContentPatchUpstream_OversizedErrorPreservesStatus(t *testing.T) {
	svc, _ := newGeminiCachedContentServiceForTest(t, http.StatusTooManyRequests,
		strings.Repeat("x", geminiCachedContentResponseMaxBytes+1))
	account := &Account{ID: 9, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
	result, err := svc.PatchUpstream(context.Background(), account,
		&GeminiCachedContent{UpstreamName: "cachedContents/up1"}, time.Hour)
	var upstreamErr *GeminiCachedContentUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Nil(t, result)
	require.Equal(t, http.StatusTooManyRequests, upstreamErr.StatusCode)
	require.Len(t, upstreamErr.Body, geminiCachedContentResponseMaxBytes)
}

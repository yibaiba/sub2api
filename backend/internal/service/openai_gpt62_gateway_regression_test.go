//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT62RawChatDropsUnsupportedSamplingAfterMapping(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra", "custom-model"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", model, stream), func(t *testing.T) {
				account := passthroughArgsFallbackAccount()
				account.Credentials["model_mapping"] = map[string]any{"public": model}
				body := []byte(fmt.Sprintf(`{"model":"public","messages":[{"role":"user","content":"hi"}],"stream":%t,"temperature":0.3,"top_p":0.8,"logprobs":true,"top_logprobs":3,"reasoning_effort":"max"}`, stream))
				captureErr := errors.New("capture outbound request")
				upstream := &httpUpstreamRecorder{err: captureErr}
				svc := &OpenAIGatewayService{cfg: passthroughArgsTestConfig(), httpUpstream: upstream}
				_, err := svc.forwardAsRawChatCompletions(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), account, body, "")
				require.Error(t, err)
				require.NotEmpty(t, upstream.lastBody)
				var outbound map[string]any
				require.NoError(t, json.Unmarshal(upstream.lastBody, &outbound))
				require.Equal(t, model, outbound["model"])
				require.Equal(t, "max", outbound["reasoning_effort"])
				require.Equal(t, stream, outbound["stream"])
				for _, field := range []string{"temperature", "top_p", "logprobs", "top_logprobs"} {
					if model == "custom-model" {
						require.Contains(t, outbound, field)
					} else {
						require.NotContains(t, outbound, field)
					}
				}
			})
		}
	}
}

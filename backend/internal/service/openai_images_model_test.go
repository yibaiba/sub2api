//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesResponsesDriverAndImageModels(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare-2026-09-08"} {
		for _, quality := range []string{"xhigh", "max", "auto"} {
			for _, endpoint := range []string{openAIImagesGenerationsEndpoint, openAIImagesEditsEndpoint} {
				parsed := &OpenAIImagesRequest{
					Endpoint:     endpoint,
					Model:        model,
					Prompt:       "draw a red cup",
					Quality:      quality,
					Size:         "1536x864",
					Background:   "transparent",
					OutputFormat: "png",
					N:            1,
				}
				if parsed.IsEdits() {
					parsed.InputImageURLs = []string{"https://example.com/input.png"}
				}
				body, err := buildOpenAIImagesResponsesRequest(parsed, model)
				require.NoError(t, err)
				require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(body, "model").String())
				require.Equal(t, model, gjson.GetBytes(body, "tools.0.model").String())
				require.Equal(t, quality, gjson.GetBytes(body, "tools.0.quality").String())
				require.Equal(t, "1536x864", gjson.GetBytes(body, "tools.0.size").String())
				require.Equal(t, "transparent", gjson.GetBytes(body, "tools.0.background").String())
				if parsed.IsEdits() {
					require.Equal(t, "edit", gjson.GetBytes(body, "tools.0.action").String())
					require.Equal(t, parsed.InputImageURLs[0], gjson.GetBytes(body, "input.0.content.1.image_url").String())
				}
			}
		}
		req := map[string]any{"model": model, "input": "draw a red cup"}
		require.True(t, normalizeOpenAIResponsesImageOnlyModel(req))
		require.Equal(t, "gpt-5.6-luna", req["model"])
		require.Equal(t, model, req["tools"].([]any)[0].(map[string]any)["model"])
		require.False(t, normalizeOpenAIResponsesImageOnlyModel(req), "a valid driver must not be overwritten")
	}
	req := map[string]any{"model": "gpt-6-astra", "tools": []any{map[string]any{"type": "image_generation", "model": "gpt-image-2.5-sunburst"}}}
	require.False(t, normalizeOpenAIResponsesImageOnlyModel(req))
	require.Equal(t, "gpt-6-astra", req["model"])
}

func TestGPTImage25PricingDoesNotUseLegacyImageRates(t *testing.T) {
	for _, model := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare-2026-09-08", "gpt-image-2.5-sunburst-2026-09-08"} {
		svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"gpt-image-2": {InputCostPerToken: 2.5e-6, OutputCostPerImageToken: 15e-6},
		}}
		p := svc.GetModelPricing(model)
		require.NotNil(t, p)
		require.Equal(t, 5e-6, p.InputCostPerToken)
		require.Equal(t, 8e-6, p.InputCostPerImageToken)
		require.Equal(t, 30e-6, p.OutputCostPerImageToken)
		require.Equal(t, 1.25e-6, p.CacheReadInputTokenCost)
		require.Zero(t, p.OutputCostPerToken)
		custom := &LiteLLMModelPricing{InputCostPerToken: 7e-6}
		svc.pricingData[model] = custom
		require.Same(t, custom, svc.GetModelPricing(model), "explicit pricing must win")
	}
}

func TestGPTImage25AccountModelPermissions(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, model := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
			for _, mapping := range []map[string]any{nil, {model: model}} {
				account := &Account{Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"model_mapping": mapping}}
				require.True(t, account.IsModelSupported(model))
				require.Equal(t, model, account.GetMappedModel(model))
			}
			restricted := &Account{Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-image-2": "gpt-image-2"}}}
			require.False(t, restricted.IsModelSupported(model), "an explicit administrator allowlist must remain restricted")
		}
	}
}

func TestGPTImage25UsagePreservesImageInputTokens(t *testing.T) {
	svc := &OpenAIGatewayService{}
	var usage OpenAIUsage
	svc.parseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.completed","response":{"tool_usage":{"image_gen":{"input_tokens":1550,"input_tokens_details":{"image_tokens":1521,"text_tokens":29},"output_tokens":515,"output_tokens_details":{"image_tokens":515}}}}}`), &usage)
	require.Equal(t, 1550, usage.InputTokens)
	require.Equal(t, 1521, usage.ImageInputTokens)
	require.Equal(t, 515, usage.ImageOutputTokens)
}

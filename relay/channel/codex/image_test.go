package codex

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

func TestBuildCodexImageRequestGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := gin.CreateTestContextOnly(httptest.NewRecorder(), gin.New())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(""))

	n := uint(2)
	outputFormat, err := common.Marshal("webp")
	if err != nil {
		t.Fatalf("marshal output format: %v", err)
	}
	request := dto.ImageRequest{
		Model:        CodexImageModelID,
		Prompt:       "draw a clean app icon",
		N:            &n,
		Size:         "1024x1024",
		Quality:      "high",
		OutputFormat: outputFormat,
	}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: CodexImageModelID,
		},
	}

	converted, err := buildCodexImageRequest(c, info, request)
	if err != nil {
		t.Fatalf("buildCodexImageRequest returned error: %v", err)
	}
	body := converted.(codexImageResponsesRequest)
	if !body.Stream {
		t.Fatal("Codex image bridge should request upstream SSE")
	}
	if body.Model != CodexImagesMainModel {
		t.Fatalf("main model = %q, want %q", body.Model, CodexImagesMainModel)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(body.Tools))
	}
	tool := body.Tools[0]
	if tool.Type != "image_generation" || tool.Action != "generate" || tool.Model != CodexImageModelID {
		t.Fatalf("unexpected tool: %+v", tool)
	}
	if tool.N == nil || *tool.N != 2 {
		t.Fatalf("tool n = %v, want 2", tool.N)
	}
	if tool.OutputFormat != "webp" {
		t.Fatalf("tool output format = %q, want webp", tool.OutputFormat)
	}
	if !info.IsStream {
		t.Fatal("relay info stream flag should be set for upstream SSE")
	}
}

func TestCollectCodexImageResponseFromSSE(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"ZmFsbGJhY2s=\",\"output_format\":\"png\"}}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"total_tokens\":7},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"revised_prompt\":\"final prompt\",\"output_format\":\"webp\",\"size\":\"1024x1024\"}]}}\n\n",
	)

	collection, err := collectCodexImageResponse(body)
	if err != nil {
		t.Fatalf("collectCodexImageResponse returned error: %v", err)
	}
	if len(collection.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(collection.Results))
	}
	if collection.Results[0].Result != "ZmluYWw=" {
		t.Fatalf("result = %q, want final image", collection.Results[0].Result)
	}
	if collection.FirstMeta.OutputFormat != "webp" {
		t.Fatalf("output format = %q, want webp", collection.FirstMeta.OutputFormat)
	}
	if collection.Usage == nil || collection.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want total tokens 7", collection.Usage)
	}
}

func TestBuildCodexImageOpenAIResponseURL(t *testing.T) {
	collection := &codexImageResponseCollection{
		CreatedAt: 1710000000,
		Results: []codexImageResult{
			{Result: "ZmluYWw=", OutputFormat: "webp", RevisedPrompt: "final prompt"},
		},
		FirstMeta: codexImageResult{OutputFormat: "webp", Size: "1024x1024"},
	}

	response := buildCodexImageOpenAIResponse(collection, "url", CodexImageModelID)
	if len(response.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(response.Data))
	}
	if response.Data[0].URL != "data:image/webp;base64,ZmluYWw=" {
		t.Fatalf("url = %q, want data URL", response.Data[0].URL)
	}
	if response.Data[0].B64JSON != "" {
		t.Fatalf("b64_json = %q, want empty for url response", response.Data[0].B64JSON)
	}
	if response.Model != CodexImageModelID {
		t.Fatalf("model = %q, want %q", response.Model, CodexImageModelID)
	}
}

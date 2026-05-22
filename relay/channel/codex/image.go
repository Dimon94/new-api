package codex

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const codexImageMaxSSELineBytes = 64 << 20

type codexImageResponsesRequest struct {
	Instructions      string                     `json:"instructions"`
	Stream            bool                       `json:"stream"`
	Reasoning         dto.Reasoning              `json:"reasoning"`
	ParallelToolCalls bool                       `json:"parallel_tool_calls"`
	Include           []string                   `json:"include"`
	Model             string                     `json:"model"`
	Store             bool                       `json:"store"`
	ToolChoice        map[string]string          `json:"tool_choice"`
	Input             []codexImageInputMessage   `json:"input"`
	Tools             []codexImageGenerationTool `json:"tools"`
}

type codexImageInputMessage struct {
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []codexImageContentPart `json:"content"`
}

type codexImageContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type codexImageGenerationTool struct {
	Type              string                `json:"type"`
	Action            string                `json:"action"`
	Model             string                `json:"model"`
	N                 *uint                 `json:"n,omitempty"`
	Size              string                `json:"size,omitempty"`
	Quality           string                `json:"quality,omitempty"`
	Background        string                `json:"background,omitempty"`
	OutputFormat      string                `json:"output_format,omitempty"`
	Moderation        string                `json:"moderation,omitempty"`
	Style             string                `json:"style,omitempty"`
	InputFidelity     string                `json:"input_fidelity,omitempty"`
	OutputCompression *int                  `json:"output_compression,omitempty"`
	PartialImages     *int                  `json:"partial_images,omitempty"`
	InputImageMask    *codexImageMaskSource `json:"input_image_mask,omitempty"`
}

type codexImageMaskSource struct {
	ImageURL string `json:"image_url"`
}

type codexImageOpenAIResponse struct {
	Created      int64                  `json:"created"`
	Data         []codexImageOpenAIData `json:"data"`
	Model        string                 `json:"model,omitempty"`
	Background   string                 `json:"background,omitempty"`
	OutputFormat string                 `json:"output_format,omitempty"`
	Quality      string                 `json:"quality,omitempty"`
	Size         string                 `json:"size,omitempty"`
}

type codexImageOpenAIData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type codexImageResponseCollection struct {
	Results       []codexImageResult
	FirstMeta     codexImageResult
	CreatedAt     int64
	Usage         *dto.Usage
	UpstreamError *types.OpenAIError
	Final         bool
	seen          map[string]struct{}
}

type codexImageResult struct {
	Result        string `json:"result"`
	RevisedPrompt string `json:"revised_prompt"`
	OutputFormat  string `json:"output_format"`
	Size          string `json:"size"`
	Background    string `json:"background"`
	Quality       string `json:"quality"`
}

type codexImageResponsesEvent struct {
	Type      string                    `json:"type"`
	Error     *codexImageResponseError  `json:"error,omitempty"`
	Response  *codexImageResponseObject `json:"response,omitempty"`
	Item      *codexImageOutputItem     `json:"item,omitempty"`
	Output    []codexImageOutputItem    `json:"output,omitempty"`
	CreatedAt int64                     `json:"created_at,omitempty"`
	Usage     *codexImageResponsesUsage `json:"usage,omitempty"`
}

type codexImageResponseObject struct {
	ID        string                    `json:"id,omitempty"`
	Status    string                    `json:"status,omitempty"`
	Error     *codexImageResponseError  `json:"error,omitempty"`
	Output    []codexImageOutputItem    `json:"output,omitempty"`
	CreatedAt int64                     `json:"created_at,omitempty"`
	Usage     *codexImageResponsesUsage `json:"usage,omitempty"`
}

type codexImageOutputItem struct {
	Type          string `json:"type"`
	Result        string `json:"result"`
	RevisedPrompt string `json:"revised_prompt"`
	OutputFormat  string `json:"output_format"`
	Size          string `json:"size"`
	Background    string `json:"background"`
	Quality       string `json:"quality"`
}

type codexImageResponseError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    any    `json:"code"`
}

type codexImageResponsesUsage struct {
	InputTokens        int                     `json:"input_tokens"`
	OutputTokens       int                     `json:"output_tokens"`
	TotalTokens        int                     `json:"total_tokens"`
	InputTokensDetails *dto.InputTokenDetails  `json:"input_tokens_details,omitempty"`
	OutputTokenDetails *dto.OutputTokenDetails `json:"output_tokens_details,omitempty"`
}

func buildCodexImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	if info.RelayMode != relayconstant.RelayModeImagesGenerations && info.RelayMode != relayconstant.RelayModeImagesEdits {
		return nil, errors.New("codex channel: image endpoint not supported")
	}

	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return nil, errors.New("codex channel: prompt is required")
	}

	images, mask, err := collectCodexImageInputs(c, request)
	if err != nil {
		return nil, err
	}
	if info.RelayMode == relayconstant.RelayModeImagesEdits && len(images) == 0 {
		return nil, errors.New("codex channel: /v1/images/edits requires at least one image")
	}

	imageModel := ""
	if info.ChannelMeta != nil {
		imageModel = strings.TrimSpace(info.UpstreamModelName)
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(request.Model)
	}
	if imageModel == "" {
		imageModel = CodexImageModelID
	}

	action := "generate"
	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		action = "edit"
	}

	content := []codexImageContentPart{{Type: "input_text", Text: prompt}}
	for _, image := range images {
		content = append(content, codexImageContentPart{Type: "input_image", ImageURL: image})
	}

	tool := codexImageGenerationTool{
		Type:              "image_generation",
		Action:            action,
		Model:             imageModel,
		Size:              strings.TrimSpace(request.Size),
		Quality:           strings.TrimSpace(request.Quality),
		Background:        rawString(request.Background),
		OutputFormat:      rawString(request.OutputFormat),
		Moderation:        rawString(request.Moderation),
		Style:             rawString(request.Style),
		OutputCompression: rawInt(request.OutputCompression),
		PartialImages:     rawInt(request.PartialImages),
	}
	if action == "edit" {
		tool.InputFidelity = rawString(request.InputFidelity)
	}
	if request.N != nil && *request.N > 1 {
		tool.N = request.N
	}
	if mask != "" {
		tool.InputImageMask = &codexImageMaskSource{ImageURL: mask}
	}

	c.Set("codex_image_response_format", normalizeCodexImageResponseFormat(request.ResponseFormat))
	c.Set("codex_image_model", imageModel)
	info.IsStream = true

	return codexImageResponsesRequest{
		Instructions:      "",
		Stream:            true,
		Reasoning:         dto.Reasoning{Effort: "medium", Summary: "auto"},
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
		Model:             CodexImagesMainModel,
		Store:             false,
		ToolChoice:        map[string]string{"type": "image_generation"},
		Input: []codexImageInputMessage{
			{
				Type:    "message",
				Role:    "user",
				Content: content,
			},
		},
		Tools: []codexImageGenerationTool{tool},
	}, nil
}

func collectCodexImageInputs(c *gin.Context, request dto.ImageRequest) ([]string, string, error) {
	seen := map[string]struct{}{}
	images := make([]string, 0)
	addImage := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		images = append(images, value)
	}

	for _, value := range rawImageValues(request.Image) {
		addImage(value)
	}
	for _, value := range rawImageValues(request.Images) {
		addImage(value)
	}

	mask := firstRawImageValue(request.Mask)
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, "", err
		}
		for _, fieldName := range []string{"image", "image[]"} {
			for _, value := range form.Value[fieldName] {
				addImage(value)
			}
			for _, fileHeader := range form.File[fieldName] {
				dataURL, err := multipartImageToDataURL(fileHeader)
				if err != nil {
					return nil, "", err
				}
				addImage(dataURL)
			}
		}
		for fieldName, files := range form.File {
			if fieldName == "image" || fieldName == "image[]" || !strings.HasPrefix(fieldName, "image[") {
				continue
			}
			for _, fileHeader := range files {
				dataURL, err := multipartImageToDataURL(fileHeader)
				if err != nil {
					return nil, "", err
				}
				addImage(dataURL)
			}
		}
		if mask == "" {
			for _, value := range form.Value["mask"] {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					mask = trimmed
					break
				}
			}
		}
		if mask == "" {
			if files := form.File["mask"]; len(files) > 0 {
				dataURL, err := multipartImageToDataURL(files[0])
				if err != nil {
					return nil, "", err
				}
				mask = dataURL
			}
		}
	}

	return images, strings.TrimSpace(mask), nil
}

func rawImageValues(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var single string
	if err := common.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}

	var many []string
	if err := common.Unmarshal(raw, &many); err == nil {
		return many
	}

	var object struct {
		ImageURL any    `json:"image_url"`
		URL      string `json:"url"`
	}
	if err := common.Unmarshal(raw, &object); err == nil {
		if value := imageValueFromObject(object.ImageURL, object.URL); value != "" {
			return []string{value}
		}
	}

	var objects []struct {
		ImageURL any    `json:"image_url"`
		URL      string `json:"url"`
	}
	if err := common.Unmarshal(raw, &objects); err == nil {
		out := make([]string, 0, len(objects))
		for _, item := range objects {
			if value := imageValueFromObject(item.ImageURL, item.URL); value != "" {
				out = append(out, value)
			}
		}
		return out
	}

	return nil
}

func imageValueFromObject(imageURL any, url string) string {
	if trimmed := strings.TrimSpace(url); trimmed != "" {
		return trimmed
	}
	switch value := imageURL.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		if url, _ := value["url"].(string); url != "" {
			return strings.TrimSpace(url)
		}
	}
	return ""
}

func firstRawImageValue(raw json.RawMessage) string {
	for _, value := range rawImageValues(raw) {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := common.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func rawInt(raw json.RawMessage) *int {
	if len(raw) == 0 {
		return nil
	}
	var value int
	if err := common.Unmarshal(raw, &value); err == nil {
		return &value
	}
	return nil
}

func multipartImageToDataURL(fileHeader *multipart.FileHeader) (string, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		contentType = "image/png"
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func handleCodexImageResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	collection, err := collectCodexImageResponse(responseBody)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if collection.UpstreamError != nil {
		return nil, types.WithOpenAIError(*collection.UpstreamError, codexImageErrorStatus(collection.UpstreamError))
	}
	if len(collection.Results) == 0 {
		return nil, types.NewOpenAIError(errors.New("codex image response missing image output"), types.ErrorCodeEmptyResponse, http.StatusBadGateway)
	}

	format := normalizeCodexImageResponseFormat(c.GetString("codex_image_response_format"))
	imageModel := c.GetString("codex_image_model")
	out := buildCodexImageOpenAIResponse(collection, format, imageModel)
	outBody, err := common.Marshal(out)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	outResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	outResp.Header.Set("Content-Type", "application/json")
	service.IOCopyBytesGracefully(c, outResp, outBody)

	usage := collection.Usage
	if usage == nil {
		usage = &dto.Usage{}
	}
	return usage, nil
}

func collectCodexImageResponse(body []byte) (*codexImageResponseCollection, error) {
	collection := &codexImageResponseCollection{seen: map[string]struct{}{}}
	if looksLikeSSE(body) {
		var parseErr error
		forEachSSEDataPayload(body, func(payload []byte) {
			if parseErr != nil || len(payload) == 0 || string(payload) == "[DONE]" {
				return
			}
			var event codexImageResponsesEvent
			if err := common.Unmarshal(payload, &event); err != nil {
				parseErr = err
				return
			}
			collection.mergeEvent(event)
		})
		if parseErr != nil {
			return nil, parseErr
		}
		return collection, nil
	}

	var event codexImageResponsesEvent
	if err := common.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	collection.mergeEvent(event)
	return collection, nil
}

func looksLikeSSE(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "data:") || strings.Contains(trimmed, "\ndata:")
}

func forEachSSEDataPayload(body []byte, fn func([]byte)) {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 1024), codexImageMaxSSELineBytes)

	dataLines := make([]string, 0, 1)
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		fn([]byte(strings.TrimSpace(payload)))
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
}

func (c *codexImageResponseCollection) mergeEvent(event codexImageResponsesEvent) {
	if event.Error != nil {
		c.UpstreamError = event.Error.toOpenAIError()
	}
	if event.Response != nil {
		if event.Response.CreatedAt > 0 {
			c.CreatedAt = event.Response.CreatedAt
		}
		if event.Response.Usage != nil {
			c.Usage = event.Response.Usage.toUsage()
		}
		if event.Response.Error != nil {
			c.UpstreamError = event.Response.Error.toOpenAIError()
		}
		final := event.Type == "response.completed"
		c.appendOutputs(event.Response.Output, final)
	}
	if event.CreatedAt > 0 {
		c.CreatedAt = event.CreatedAt
	}
	if event.Usage != nil {
		c.Usage = event.Usage.toUsage()
	}
	if event.Item != nil && event.Item.Type == "image_generation_call" && event.Item.Result != "" && !c.Final {
		c.appendResults([]codexImageResult{event.Item.toResult()}, false)
	}
	if len(event.Output) > 0 {
		c.appendOutputs(event.Output, event.Type == "response.completed")
	}
}

func (c *codexImageResponseCollection) appendOutputs(outputs []codexImageOutputItem, final bool) {
	results := make([]codexImageResult, 0, len(outputs))
	for _, output := range outputs {
		if output.Type != "image_generation_call" || strings.TrimSpace(output.Result) == "" {
			continue
		}
		results = append(results, output.toResult())
	}
	c.appendResults(results, final)
}

func (c *codexImageResponseCollection) appendResults(results []codexImageResult, final bool) {
	if len(results) == 0 {
		return
	}
	if final {
		c.Final = true
		c.Results = nil
		c.seen = map[string]struct{}{}
	}
	for _, result := range results {
		result.Result = strings.TrimSpace(result.Result)
		if result.Result == "" {
			continue
		}
		if _, ok := c.seen[result.Result]; ok {
			continue
		}
		c.seen[result.Result] = struct{}{}
		if len(c.Results) == 0 {
			c.FirstMeta = result
		}
		c.Results = append(c.Results, result)
	}
}

func (o codexImageOutputItem) toResult() codexImageResult {
	return codexImageResult{
		Result:        strings.TrimSpace(o.Result),
		RevisedPrompt: strings.TrimSpace(o.RevisedPrompt),
		OutputFormat:  strings.TrimSpace(o.OutputFormat),
		Size:          strings.TrimSpace(o.Size),
		Background:    strings.TrimSpace(o.Background),
		Quality:       strings.TrimSpace(o.Quality),
	}
}

func (u *codexImageResponsesUsage) toUsage() *dto.Usage {
	if u == nil {
		return nil
	}
	usage := &dto.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if u.InputTokensDetails != nil {
		usage.InputTokensDetails = u.InputTokensDetails
		usage.PromptTokensDetails.CachedTokens = u.InputTokensDetails.CachedTokens
		usage.PromptTokensDetails.TextTokens = u.InputTokensDetails.TextTokens
		usage.PromptTokensDetails.ImageTokens = u.InputTokensDetails.ImageTokens
	}
	if u.OutputTokenDetails != nil {
		usage.CompletionTokenDetails = *u.OutputTokenDetails
	}
	return usage
}

func (e *codexImageResponseError) toOpenAIError() *types.OpenAIError {
	if e == nil {
		return nil
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "Codex image generation failed"
	}
	errType := strings.TrimSpace(e.Type)
	if errType == "" {
		errType = "upstream_error"
	}
	return &types.OpenAIError{
		Message: message,
		Type:    errType,
		Param:   strings.TrimSpace(e.Param),
		Code:    e.Code,
	}
}

func buildCodexImageOpenAIResponse(collection *codexImageResponseCollection, responseFormat string, model string) codexImageOpenAIResponse {
	createdAt := collection.CreatedAt
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	out := codexImageOpenAIResponse{
		Created:      createdAt,
		Data:         make([]codexImageOpenAIData, 0, len(collection.Results)),
		Model:        strings.TrimSpace(model),
		Background:   collection.FirstMeta.Background,
		OutputFormat: collection.FirstMeta.OutputFormat,
		Quality:      collection.FirstMeta.Quality,
		Size:         collection.FirstMeta.Size,
	}
	for _, image := range collection.Results {
		item := codexImageOpenAIData{RevisedPrompt: image.RevisedPrompt}
		if strings.EqualFold(responseFormat, "url") {
			item.URL = fmt.Sprintf("data:%s;base64,%s", codexImageMIMEType(image.OutputFormat), image.Result)
		} else {
			item.B64JSON = image.Result
		}
		out.Data = append(out.Data, item)
	}
	return out
}

func normalizeCodexImageResponseFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "url" {
		return "url"
	}
	return "b64_json"
}

func codexImageMIMEType(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func codexImageErrorStatus(err *types.OpenAIError) int {
	if err == nil {
		return http.StatusBadGateway
	}
	code := strings.ToLower(strings.TrimSpace(fmt.Sprint(err.Code)))
	errType := strings.ToLower(strings.TrimSpace(err.Type))
	if code == "moderation_blocked" || strings.Contains(errType, "image_generation_user_error") {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

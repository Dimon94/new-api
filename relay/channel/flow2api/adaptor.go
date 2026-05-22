package flow2api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const ChannelName = "flow2api"

var ModelList = []string{
	"gemini-3.0-pro-image",
	"gemini-3.0-pro-image-landscape",
	"gemini-3.0-pro-image-portrait",
	"gemini-3.0-pro-image-square",
	"gemini-3.0-pro-image-four-three",
	"gemini-3.0-pro-image-three-four",
	"gemini-3.0-pro-image-landscape-2k",
	"gemini-3.0-pro-image-portrait-2k",
	"gemini-3.0-pro-image-square-2k",
	"gemini-3.0-pro-image-four-three-2k",
	"gemini-3.0-pro-image-three-four-2k",
	"gemini-3.0-pro-image-landscape-4k",
	"gemini-3.0-pro-image-portrait-4k",
	"gemini-3.0-pro-image-square-4k",
	"gemini-3.0-pro-image-four-three-4k",
	"gemini-3.0-pro-image-three-four-4k",
	"imagen-4.0-generate-preview",
	"imagen-4.0-generate-preview-landscape",
	"imagen-4.0-generate-preview-portrait",
	"gemini-3.1-flash-image",
	"gemini-3.1-flash-image-landscape",
	"gemini-3.1-flash-image-portrait",
	"gemini-3.1-flash-image-square",
	"gemini-3.1-flash-image-four-three",
	"gemini-3.1-flash-image-three-four",
	"gemini-3.1-flash-image-landscape-2k",
	"gemini-3.1-flash-image-portrait-2k",
	"gemini-3.1-flash-image-square-2k",
	"gemini-3.1-flash-image-four-three-2k",
	"gemini-3.1-flash-image-three-four-2k",
	"gemini-3.1-flash-image-landscape-4k",
	"gemini-3.1-flash-image-portrait-4k",
	"gemini-3.1-flash-image-square-4k",
	"gemini-3.1-flash-image-four-three-4k",
	"gemini-3.1-flash-image-three-four-4k",
	"veo_3_1_t2v_fast_landscape",
	"veo_3_1_t2v_fast_portrait",
	"veo_3_1_i2v_s_fast_fl",
	"veo_3_1_i2v_s_fast_portrait_fl",
	"veo_3_1_interpolation_lite_landscape",
	"veo_3_1_interpolation_lite_portrait",
	"veo_3_1_r2v_fast_landscape",
	"veo_3_1_r2v_fast_portrait",
}

type Adaptor struct {
	apiKey  string
	baseURL string
}

type bridgeImageRequest struct {
	Model          string   `json:"model"`
	Prompt         string   `json:"prompt"`
	Images         []string `json:"images,omitempty"`
	Mask           string   `json:"mask,omitempty"`
	N              *uint    `json:"n,omitempty"`
	Size           string   `json:"size,omitempty"`
	Quality        string   `json:"quality,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
	a.apiKey = info.ApiKey
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.RelayMode != relayconstant.RelayModeImagesGenerations && info.RelayMode != relayconstant.RelayModeImagesEdits {
		return "", errors.New("flow2api channel: endpoint not supported")
	}
	if a.baseURL == "" {
		return "", errors.New("flow2api channel: base url is empty")
	}
	return relaycommon.GetFullRequestURL(a.baseURL, "/api/bridge/images", info.ChannelType), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	req.Set("Authorization", "Bearer "+a.apiKey)
	req.Set("Content-Type", "application/json")
	req.Set("Accept", "application/json")
	return nil
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	images, mask, err := collectImageInputs(c, request)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(request.Model)
	if info.ChannelMeta != nil && strings.TrimSpace(info.UpstreamModelName) != "" {
		model = strings.TrimSpace(info.UpstreamModelName)
	}
	return bridgeImageRequest{
		Model:          model,
		Prompt:         request.Prompt,
		Images:         images,
		Mask:           mask,
		N:              request.N,
		Size:           request.Size,
		Quality:        request.Quality,
		ResponseFormat: request.ResponseFormat,
	}, nil
}

func collectImageInputs(c *gin.Context, request dto.ImageRequest) ([]string, string, error) {
	seen := map[string]struct{}{}
	images := make([]string, 0)
	add := func(value string) {
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
		add(value)
	}
	for _, value := range rawImageValues(request.Images) {
		add(value)
	}

	mask := firstRawImageValue(request.Mask)
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, "", err
		}
		for _, fieldName := range []string{"image", "image[]"} {
			for _, value := range form.Value[fieldName] {
				add(value)
			}
			for _, fileHeader := range form.File[fieldName] {
				dataURL, err := multipartImageToDataURL(fileHeader)
				if err != nil {
					return nil, "", err
				}
				add(dataURL)
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
				add(dataURL)
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

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	if info.RelayMode != relayconstant.RelayModeImagesGenerations && info.RelayMode != relayconstant.RelayModeImagesEdits {
		return nil, types.NewError(errors.New("flow2api channel: endpoint not supported"), types.ErrorCodeInvalidRequest)
	}
	return openai.OpenaiHandlerWithUsage(c, info, resp)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("flow2api channel: /v1/chat/completions endpoint not supported")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("flow2api channel: /v1/rerank endpoint not supported")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("flow2api channel: /v1/embeddings endpoint not supported")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("flow2api channel: audio endpoint not supported")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("flow2api channel: /v1/responses endpoint not supported")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("flow2api channel: /v1/messages endpoint not supported")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("flow2api channel: Gemini endpoint not supported")
}

var _ channel.Adaptor = (*Adaptor)(nil)

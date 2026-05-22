package flow2api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

type imageURL struct {
	URL string `json:"url"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type bridgeSubmitRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type bridgeVideoError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type bridgeVideoResponse struct {
	ID          string            `json:"id"`
	Object      string            `json:"object"`
	Model       string            `json:"model"`
	Status      string            `json:"status"`
	Progress    int               `json:"progress"`
	URL         string            `json:"url,omitempty"`
	CreatedAt   int64             `json:"created_at,omitempty"`
	CompletedAt int64             `json:"completed_at,omitempty"`
	Error       *bridgeVideoError `json:"error,omitempty"`
}

type bridgeErrorResponse struct {
	Error *bridgeVideoError `json:"error,omitempty"`
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
	a.apiKey = info.ApiKey
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionTextGenerate); taskErr != nil {
		return taskErr
	}

	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	switch {
	case len(req.Images) >= 2:
		info.Action = constant.TaskActionFirstTailGenerate
	case len(req.Images) == 1:
		info.Action = constant.TaskActionGenerate
	default:
		info.Action = constant.TaskActionTextGenerate
	}
	return nil
}

func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}

	seconds := req.Duration
	if seconds == 0 && req.Seconds != "" {
		seconds, _ = strconv.Atoi(req.Seconds)
	}
	if seconds <= 0 {
		seconds = 8
	}

	ratios := map[string]float64{
		"seconds": float64(seconds),
	}
	if strings.Contains(info.OriginModelName, "_4k") {
		ratios["resolution"] = 4
	} else if strings.Contains(info.OriginModelName, "_1080p") {
		ratios["resolution"] = 2
	}
	return ratios
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if a.baseURL == "" {
		return "", fmt.Errorf("flow2api base url is empty")
	}
	return a.baseURL + "/api/bridge/videos", nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}

	parts := make([]contentPart, 0, len(req.Images)+1)
	if strings.TrimSpace(req.Prompt) != "" {
		parts = append(parts, contentPart{
			Type: "text",
			Text: req.Prompt,
		})
	}
	for _, image := range req.Images {
		if strings.TrimSpace(image) == "" {
			continue
		}
		parts = append(parts, contentPart{
			Type:     "image_url",
			ImageURL: &imageURL{URL: image},
		})
	}

	var content any = req.Prompt
	if len(parts) > 0 {
		content = parts
	}
	model := strings.TrimSpace(req.Model)
	if info.ChannelMeta != nil && strings.TrimSpace(info.UpstreamModelName) != "" {
		model = strings.TrimSpace(info.UpstreamModelName)
	}
	body := bridgeSubmitRequest{
		Model: model,
		Messages: []chatMessage{
			{
				Role:    "user",
				Content: content,
			},
		},
		Stream: false,
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var errResp bridgeErrorResponse
	if err := common.Unmarshal(responseBody, &errResp); err == nil && errResp.Error != nil {
		return "", responseBody, service.TaskErrorWrapperLocal(fmt.Errorf("%s", errResp.Error.Message), "flow2api_bridge_error", resp.StatusCode)
	}

	var bridgeResp bridgeVideoResponse
	if err := common.Unmarshal(responseBody, &bridgeResp); err != nil {
		return "", responseBody, service.TaskErrorWrapper(err, "unmarshal_response_failed", http.StatusInternalServerError)
	}
	if bridgeResp.ID == "" {
		return "", responseBody, service.TaskErrorWrapper(fmt.Errorf("missing flow2api bridge task id"), "invalid_response", http.StatusInternalServerError)
	}

	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = info.PublicTaskID
	openAIVideo.TaskID = info.PublicTaskID
	openAIVideo.Model = info.OriginModelName
	openAIVideo.CreatedAt = time.Now().Unix()
	openAIVideo.Status = dto.VideoStatusQueued
	c.JSON(http.StatusOK, openAIVideo)

	return bridgeResp.ID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/api/bridge/videos/" + url.PathEscape(taskID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var errResp bridgeErrorResponse
	if err := common.Unmarshal(respBody, &errResp); err == nil && errResp.Error != nil {
		return relaycommon.FailTaskInfo(errResp.Error.Message), nil
	}

	var bridgeResp bridgeVideoResponse
	if err := common.Unmarshal(respBody, &bridgeResp); err != nil {
		return nil, fmt.Errorf("unmarshal bridge response failed: %w", err)
	}

	taskInfo := &relaycommon.TaskInfo{
		TaskID: bridgeResp.ID,
		Url:    bridgeResp.URL,
	}
	switch bridgeResp.Status {
	case "queued":
		taskInfo.Status = model.TaskStatusQueued
		taskInfo.Progress = taskcommon.ProgressQueued
	case "in_progress", "processing":
		taskInfo.Status = model.TaskStatusInProgress
		if bridgeResp.Progress > 0 {
			taskInfo.Progress = fmt.Sprintf("%d%%", bridgeResp.Progress)
		}
	case "completed", "succeeded", "success":
		taskInfo.Status = model.TaskStatusSuccess
		taskInfo.Progress = taskcommon.ProgressComplete
	case "failed", "failure":
		taskInfo.Status = model.TaskStatusFailure
		taskInfo.Progress = taskcommon.ProgressComplete
		if bridgeResp.Error != nil {
			taskInfo.Reason = bridgeResp.Error.Message
		}
	default:
		taskInfo.Status = model.TaskStatusInProgress
	}
	return taskInfo, nil
}

func (a *TaskAdaptor) GetModelList() []string {
	return []string{
		"veo_3_1_t2v_fast_landscape",
		"veo_3_1_t2v_fast_portrait",
		"veo_3_1_i2v_s_fast_fl",
		"veo_3_1_i2v_s_fast_portrait_fl",
		"veo_3_1_interpolation_lite_landscape",
		"veo_3_1_interpolation_lite_portrait",
		"veo_3_1_r2v_fast_landscape",
		"veo_3_1_r2v_fast_portrait",
	}
}

func (a *TaskAdaptor) GetChannelName() string {
	return "flow2api"
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = task.TaskID
	openAIVideo.TaskID = task.TaskID
	openAIVideo.Model = task.Properties.OriginModelName
	openAIVideo.Status = task.Status.ToVideoStatus()
	openAIVideo.SetProgressStr(task.Progress)
	openAIVideo.CreatedAt = task.SubmitTime
	openAIVideo.CompletedAt = task.FinishTime
	if url := task.GetResultURL(); url != "" {
		openAIVideo.SetMetadata("url", url)
	}
	if task.Status == model.TaskStatusFailure {
		openAIVideo.Error = &dto.OpenAIVideoError{
			Message: task.FailReason,
			Code:    "generation_failed",
		}
	}
	return common.Marshal(openAIVideo)
}

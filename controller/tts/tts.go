package tts

import (
	"GopherAI/common/code"
	"GopherAI/common/tts"
	"GopherAI/controller"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type TTSRequest struct {
	Text           string `json:"text"`
	Format         string `json:"format,omitempty"`
	Voice          int    `json:"voice,omitempty"`
	Language       string `json:"lang,omitempty"`
	Speed          int    `json:"speed,omitempty"`
	Pitch          int    `json:"pitch,omitempty"`
	Volume         int    `json:"volume,omitempty"`
	EnableSubtitle int    `json:"enable_subtitle,omitempty"`
}

type TTSResponse struct {
	TaskID string `json:"task_id,omitempty"`
	controller.Response
}

type QueryTTSResponse struct {
	TaskID     string `json:"task_id,omitempty"`
	TaskStatus string `json:"task_status,omitempty"`
	TaskResult string `json:"task_result,omitempty"`
	controller.Response
}

var (
	serviceOnce sync.Once
	service     *tts.TTSService
)

func defaultService() *tts.TTSService {
	serviceOnce.Do(func() { service = tts.NewTTSService() })
	return service
}

func CreateTTSTask(c *gin.Context) {
	req := new(TTSRequest)
	res := new(TTSResponse)
	if err := c.ShouldBindJSON(req); err != nil || strings.TrimSpace(req.Text) == "" {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	taskID, err := defaultService().CreateTTSForUser(c.Request.Context(), c.GetString("userName"), req.Text, tts.SynthesisOptions{
		Format:         req.Format,
		Voice:          req.Voice,
		Language:       req.Language,
		Speed:          req.Speed,
		Pitch:          req.Pitch,
		Volume:         req.Volume,
		EnableSubtitle: req.EnableSubtitle,
	})
	if err != nil {
		log.Printf("TTS create failed: %v", err)
		c.JSON(http.StatusOK, res.CodeOf(code.TTSFail))
		return
	}
	res.Success()
	res.TaskID = taskID
	c.JSON(http.StatusOK, res)
}

func QueryTTSTask(c *gin.Context) {
	res := new(QueryTTSResponse)
	taskID := strings.TrimSpace(c.Query("task_id"))
	if taskID == "" {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	queryResponse, err := defaultService().QueryTTSForUser(c.Request.Context(), c.GetString("userName"), taskID)
	if err != nil {
		log.Printf("TTS query failed: %v", err)
		c.JSON(http.StatusOK, res.CodeOf(code.TTSFail))
		return
	}
	if len(queryResponse.TasksInfo) == 0 {
		c.JSON(http.StatusOK, res.CodeOf(code.TTSFail))
		return
	}
	res.Success()
	res.TaskID = queryResponse.TasksInfo[0].TaskID
	res.TaskStatus = queryResponse.TasksInfo[0].TaskStatus
	if queryResponse.TasksInfo[0].TaskResult != nil {
		res.TaskResult = queryResponse.TasksInfo[0].TaskResult.SpeechURL
	}
	c.JSON(http.StatusOK, res)
}

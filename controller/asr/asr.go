package asr

import (
	"GopherAI/common/asr"
	"GopherAI/common/code"
	"GopherAI/controller"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

const multipartOverhead = 1 << 20

type RecognizeResponse struct {
	Text string `json:"text,omitempty"`
	controller.Response
}

var (
	serviceOnce sync.Once
	service     *asr.Service
)

func defaultService() *asr.Service {
	serviceOnce.Do(func() { service = asr.NewService() })
	return service
}

func Recognize(c *gin.Context) {
	res := new(RecognizeResponse)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, asr.MaxAudioBytes+multipartOverhead)
	fileHeader, err := c.FormFile("file")
	if err != nil || fileHeader.Size <= 0 || fileHeader.Size > asr.MaxAudioBytes {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	format, err := asr.DetectFormat(fileHeader.Filename, fileHeader.Header.Get("Content-Type"))
	if err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, asr.MaxAudioBytes+1))
	if err != nil || len(audio) == 0 || len(audio) > asr.MaxAudioBytes {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}

	text, err := defaultService().Recognize(c.Request.Context(), asr.RecognitionRequest{
		Audio:      audio,
		Format:     format,
		SampleRate: 16000,
		Username:   c.GetString("userName"),
	})
	if err != nil {
		log.Printf("ASR request failed: %v", err)
		c.JSON(http.StatusOK, res.CodeOf(code.ASRFail))
		return
	}
	res.Success()
	res.Text = text
	c.JSON(http.StatusOK, res)
}

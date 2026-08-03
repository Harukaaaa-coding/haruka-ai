package image

import (
	"GopherAI/common/code"
	"GopherAI/controller"
	imageservice "GopherAI/service/image"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

const multipartOverhead int64 = 1 << 20

type (
	RecognizeImageResponse struct {
		ClassName string `json:"class_name,omitempty"` // AI回答
		controller.Response
	}
)

func RecognizeImage(c *gin.Context) {
	res := new(RecognizeImageResponse)
	requestLimit := imageservice.MaxImageBytes + multipartOverhead
	if c.Request.ContentLength > requestLimit {
		c.JSON(http.StatusRequestEntityTooLarge, res.CodeOf(code.CodeInvalidParams))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, requestLimit)
	file, err := c.FormFile("image")
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			c.JSON(http.StatusRequestEntityTooLarge, res.CodeOf(code.CodeInvalidParams))
			return
		}
		c.JSON(http.StatusBadRequest, res.CodeOf(code.CodeInvalidParams))
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if file.Size <= 0 {
		c.JSON(http.StatusBadRequest, res.CodeOf(code.CodeInvalidParams))
		return
	}
	if file.Size > imageservice.MaxImageBytes {
		c.JSON(http.StatusRequestEntityTooLarge, res.CodeOf(code.CodeInvalidParams))
		return
	}

	className, err := imageservice.RecognizeImage(file)
	if err != nil {
		switch {
		case imageservice.IsTooLarge(err):
			c.JSON(http.StatusRequestEntityTooLarge, res.CodeOf(code.CodeInvalidParams))
		case imageservice.IsInvalidUpload(err):
			c.JSON(http.StatusBadRequest, res.CodeOf(code.CodeInvalidParams))
		default:
			log.Printf("image recognition failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, res.CodeOf(code.CodeServerBusy))
		}
		return
	}

	res.Success()
	res.ClassName = className
	c.JSON(http.StatusOK, res)
}

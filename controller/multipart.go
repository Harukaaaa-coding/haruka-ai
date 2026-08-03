package controller

import (
	"errors"
	"mime/multipart"
	"net/http"

	"github.com/gin-gonic/gin"
)

// FormFileWithLimit applies a hard limit to the complete multipart request,
// including its envelope, before Gin parses the form or creates temp files.
func FormFileWithLimit(c *gin.Context, field string, maxBytes int64) (*multipart.FileHeader, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	if c.Request.ContentLength > maxBytes {
		return nil, &http.MaxBytesError{Limit: maxBytes}
	}
	return c.FormFile(field)
}

func IsRequestBodyTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

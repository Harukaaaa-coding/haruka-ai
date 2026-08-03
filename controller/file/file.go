package file

import (
	"GopherAI/common/code"
	"GopherAI/controller"
	"GopherAI/service/file"
	knowledgebaseservice "GopherAI/service/knowledgebase"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

const multipartEnvelopeBytes int64 = 1 << 20

type (
	UploadFileResponse struct {
		FilePath string `json:"file_path,omitempty"`
		controller.Response
	}
)

func UploadRagFile(c *gin.Context) {
	res := new(UploadFileResponse)
	uploadedFile, err := controller.FormFileWithLimit(
		c, "file", knowledgebaseservice.MaxDocumentBytes+multipartEnvelopeBytes,
	)
	if err != nil {
		if controller.IsRequestBodyTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, res.CodeOf(code.CodeInvalidParams))
			return
		}
		log.Println("FormFile fail ", err)
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}

	username := c.GetString("userName")
	if username == "" {
		log.Println("Username not found in context")
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidToken))
		return
	}

	//indexer 会在 service 层根据实际文件名创建
	filePath, err := file.UploadRagFile(username, uploadedFile)
	if err != nil {
		log.Println("UploadFile fail ", err)
		c.JSON(http.StatusOK, res.CodeOf(code.CodeServerBusy))
		return
	}

	res.Success()
	res.FilePath = filePath
	c.JSON(http.StatusOK, res)
}

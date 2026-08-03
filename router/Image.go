package router

import (
	"GopherAI/controller/image"
	"GopherAI/middleware/costguard"

	"github.com/gin-gonic/gin"
)

func ImageRouter(r *gin.RouterGroup, guard *costguard.Guard) {

	r.POST("/recognize", guard.Multipart(costguard.ActionImage), image.RecognizeImage)
}

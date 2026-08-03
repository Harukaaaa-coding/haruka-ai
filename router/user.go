package router

import (
	"GopherAI/controller/user"

	"github.com/gin-gonic/gin"
)

func RegisterUserRouter(r *gin.RouterGroup) {
	{
		r.POST("/register", user.Register)
		r.POST("/login", user.Login)
		r.POST("/captcha", user.HandleCaptcha)
	}
}

func RegisterAuthenticatedUserRouter(r *gin.RouterGroup) {
	r.GET("/session", user.Session)
	r.POST("/logout", user.Logout)
}

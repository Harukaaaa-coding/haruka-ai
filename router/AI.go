package router

import (
	"GopherAI/controller/asr"
	"GopherAI/controller/modelcatalog"
	"GopherAI/controller/session"
	"GopherAI/controller/tts"
	"GopherAI/controller/voice"
	"GopherAI/middleware/costguard"

	"github.com/gin-gonic/gin"
)

func AIRouter(r *gin.RouterGroup, guard *costguard.Guard) {
	r.GET("/models", modelcatalog.GetModels)

	// 聊天相关接口
	{
		r.GET("/chat/sessions", session.GetUserSessionsByUserName)
		r.POST("/chat/send-new-session", guard.JSON(costguard.ActionChat), session.CreateSessionAndSendMessage)
		r.POST("/chat/send", guard.JSON(costguard.ActionChat), session.ChatSend)
		r.POST("/chat/history", session.ChatHistory)

		// TTS相关接口
		r.POST("/chat/tts", guard.JSON(costguard.ActionVoice), tts.CreateTTSTask)
		r.GET("/chat/tts/query", tts.QueryTTSTask)
		r.POST("/chat/asr", guard.Multipart(costguard.ActionVoice), asr.Recognize)

		r.POST("/chat/send-stream-new-session", guard.JSON(costguard.ActionChat), session.CreateStreamSessionAndSendMessage)
		r.POST("/chat/send-stream", guard.JSON(costguard.ActionChat), session.ChatStreamSend)

		// Realtime voice is a bidirectional WebSocket. It deliberately does
		// not use the request-lifetime cost guard: the voice hub applies
		// connection/audio bounds instead of holding a normal HTTP slot for the
		// complete lifetime of a WebSocket.
		r.GET("/voice/realtime", voice.Realtime)
	}

}

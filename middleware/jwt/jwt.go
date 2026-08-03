package jwt

import (
	"GopherAI/common/code"
	"GopherAI/common/sessionauth"
	"GopherAI/controller"
	"GopherAI/utils/myjwt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// 读取jwt
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		res := new(controller.Response)

		authHeader := c.GetHeader("Authorization")
		// Query-string credentials leak through browser history, proxy logs and
		// Referer headers. Accept tokens only in the standard header.
		token := ""
		authSource := "bearer"
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		}
		if token == "" {
			token = sessionauth.SessionToken(c.Request)
			if token != "" {
				authSource = "cookie"
			}
		}

		if token == "" {
			c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidToken))
			c.Abort()
			return
		}

		identity, ok := myjwt.ParseTokenIdentity(token)
		if !ok {
			c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidToken))
			c.Abort()
			return
		}
		if authSource == "cookie" && sessionauth.RequiresCSRF(c.Request) && !sessionauth.ValidCSRF(c.Request) {
			c.JSON(http.StatusForbidden, res.CodeOf(code.CodeForbidden))
			c.Abort()
			return
		}

		c.Set("userName", identity.Username)
		c.Set("userID", identity.UserID)
		c.Set("authSource", authSource)
		// tokenID carries the JWT jti claim. It is intentionally placed in the
		// request context now so a revocation store can be introduced later
		// without changing the login response or protected-route contract.
		c.Set("tokenID", identity.TokenID)
		c.Next()
	}
}

package user

import (
	"GopherAI/common/code"
	"GopherAI/common/sessionauth"
	"GopherAI/config"
	"GopherAI/controller"
	"GopherAI/service/user"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type (
	//这里的Username只能是账号登录，和我做的另一个项目有区别（邮箱账号均可)
	LoginRequest struct {
		Username string `json:"username" binding:"required,max=50"`
		Password string `json:"password" binding:"required,min=6,max=128"`
	}
	// omitempty当字段为空的时候，不返回这个东西
	LoginResponse struct {
		controller.Response
		Token string `json:"token,omitempty"`
	}
	//验证码由后端生成，存放到redis中，固然需要先发送一次请求CaptchaRequest,然后用返回的验证码
	//邮箱以及密码进行注册，后续再将账号进行返回
	RegisterRequest struct {
		Email    string `json:"email" binding:"required,email,max=100"`
		Captcha  string `json:"captcha" binding:"required,len=6,numeric"`
		Password string `json:"password" binding:"required,min=6,max=128"`
	}
	//注册成功之后，直接让其进行登录状态
	RegisterResponse struct {
		controller.Response
		Token              string `json:"token,omitempty"`
		Username           string `json:"username,omitempty"`
		SessionEstablished bool   `json:"session_established"`
	}

	SessionResponse struct {
		controller.Response
		Username string `json:"username,omitempty"`
	}

	CaptchaRequest struct {
		Email string `json:"email" binding:"required,email,max=100"`
	}

	CaptchaResponse struct {
		controller.Response
	}
)

func Login(c *gin.Context) {

	req := new(LoginRequest)
	res := new(LoginResponse)
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	allowed, retryAfter, err := user.AllowLoginAttempt(c.Request.Context(), c.ClientIP(), req.Username)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, res.CodeOf(code.CodeServerBusy))
		return
	}
	if !allowed {
		writeRateLimit(c, &res.Response, retryAfter)
		return
	}

	token, code_ := user.Login(req.Username, req.Password)
	if code_ != code.CodeSuccess {
		c.JSON(http.StatusOK, res.CodeOf(code_))
		return
	}

	if err := issueCookieSession(c, token); err != nil {
		log.Printf("issue login session cookie: %v", err)
		c.JSON(http.StatusServiceUnavailable, res.CodeOf(code.CodeServerBusy))
		return
	}
	res.Success()
	if !sessionauth.WantsCookieSession(c.Request) {
		res.Token = token
	}
	if err := user.ClearLoginAccountLimit(c.Request.Context(), req.Username); err != nil {
		log.Printf("clear login account rate limit: %v", err)
	}
	c.JSON(http.StatusOK, res)

}

func Register(c *gin.Context) {

	req := new(RegisterRequest)
	res := new(RegisterResponse)
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	allowed, retryAfter, err := user.AllowCaptchaVerification(c.Request.Context(), c.ClientIP(), req.Email)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, res.CodeOf(code.CodeServerBusy))
		return
	}
	if !allowed {
		writeRateLimit(c, &res.Response, retryAfter)
		return
	}

	token, username, code_ := user.Register(c.Request.Context(), req.Email, req.Password, req.Captcha)
	if code_ != code.CodeSuccess {
		c.JSON(http.StatusOK, res.CodeOf(code_))
		return
	}

	res.Success()
	res.Username = username
	// Registration may have committed successfully even when immediately
	// minting a login token failed. Preserve that durable success and let the
	// browser route to login instead of reporting a misleading server error.
	if token != "" {
		if err := issueCookieSession(c, token); err != nil {
			log.Printf("issue registration session cookie: %v", err)
			c.JSON(http.StatusServiceUnavailable, res.CodeOf(code.CodeServerBusy))
			return
		}
		res.SessionEstablished = true
		if !sessionauth.WantsCookieSession(c.Request) {
			res.Token = token
		}
	}
	c.JSON(http.StatusOK, res)
}

// Session returns only the current identity. It lets the SPA restore an
// HttpOnly cookie session after a page reload without exposing the JWT.
func Session(c *gin.Context) {
	res := new(SessionResponse)
	if username, ok := c.Get("userName"); ok {
		res.Username, _ = username.(string)
	}
	res.Success()
	c.JSON(http.StatusOK, res)
}

func Logout(c *gin.Context) {
	sessionauth.Clear(c.Writer, cookieSecure(c))
	res := new(controller.Response)
	res.Success()
	c.JSON(http.StatusOK, res)
}

func HandleCaptcha(c *gin.Context) {
	req := new(CaptchaRequest)
	res := new(CaptchaResponse)
	//解析参数
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	allowed, retryAfter, err := user.AllowCaptchaRequest(c.Request.Context(), c.ClientIP(), req.Email)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, res.CodeOf(code.CodeServerBusy))
		return
	}
	if !allowed {
		writeRateLimit(c, &res.Response, retryAfter)
		return
	}

	//给service层进行处理
	code_ := user.SendCaptcha(c.Request.Context(), req.Email)
	if code_ != code.CodeSuccess {
		c.JSON(http.StatusOK, res.CodeOf(code_))
		return
	}
	//匿名字段，其实本身res.Success()调用就是res.Response.Success()
	//res.Response.Success()
	res.Success()
	c.JSON(http.StatusOK, res)
}

func writeRateLimit(c *gin.Context, response *controller.Response, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	c.JSON(http.StatusTooManyRequests, response.CodeOf(code.CodeTooManyRequests))
}

func issueCookieSession(c *gin.Context, token string) error {
	if !sessionauth.WantsCookieSession(c.Request) {
		return nil
	}
	conf := config.GetConfig()
	if conf == nil || conf.ExpireDuration <= 0 {
		return fmt.Errorf("session expiration configuration is unavailable")
	}
	return sessionauth.Issue(
		c.Writer,
		token,
		time.Duration(conf.ExpireDuration)*time.Hour,
		cookieSecure(c),
	)
}

func cookieSecure(c *gin.Context) bool {
	if c == nil {
		return config.IsProduction()
	}
	return sessionauth.ShouldUseSecureCookie(c.Request, config.IsProduction())
}

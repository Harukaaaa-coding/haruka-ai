package user

import (
	"GopherAI/common/code"
	myemail "GopherAI/common/email"
	myredis "GopherAI/common/redis"
	"GopherAI/dao/user"
	"GopherAI/model"
	"GopherAI/utils"
	"GopherAI/utils/myjwt"
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func Login(username, password string) (string, code.Code) {
	username = strings.TrimSpace(username)
	var userInformation *model.User
	//1:判断用户是否存在
	userInformation, err := user.FindUserByUsername(username)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Keep the observable work and response equivalent to a wrong password
		// so callers cannot enumerate registered usernames.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
		return "", code.CodeInvalidPassword
	}
	if err != nil {
		return "", code.CodeServerBusy
	}
	if userInformation == nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
		return "", code.CodeInvalidPassword
	}
	//2:判断用户是否密码账号正确
	legacyHash := utils.MD5(password)
	legacyMatch := len(userInformation.Password) == len(legacyHash) &&
		subtle.ConstantTimeCompare([]byte(userInformation.Password), []byte(legacyHash)) == 1
	bcryptMatch := bcrypt.CompareHashAndPassword([]byte(userInformation.Password), []byte(password)) == nil
	if !bcryptMatch && !legacyMatch {
		return "", code.CodeInvalidPassword
	}
	if legacyMatch {
		if err := user.UpgradePasswordHash(userInformation.ID, password); err != nil {
			log.Printf("failed to upgrade legacy password hash for user %d: %v", userInformation.ID, err)
		}
	}
	//3:返回一个Token
	token, err := myjwt.GenerateToken(userInformation.ID, userInformation.Username)

	if err != nil {
		return "", code.CodeServerBusy
	}
	return token, code.CodeSuccess
}

func Register(ctx context.Context, email, password, captcha string) (string, string, code.Code) {
	email = strings.ToLower(strings.TrimSpace(email))

	var userInformation *model.User

	//1:先判断用户是否已经存在了
	if exists, _, err := user.EmailExists(email); err != nil {
		return "", "", code.CodeServerBusy
	} else if exists {
		return "", "", code.CodeUserExist
	}

	//2:从redis中验证验证码是否有效
	if ok, err := myredis.CheckCaptchaForEmail(ctx, email, captcha); err != nil {
		return "", "", code.CodeServerBusy
	} else if !ok {
		return "", "", code.CodeInvalidCaptcha
	}

	//3：生成11位的账号
	username := ""
	var createErr error
	for attempt := 0; attempt < 5; attempt++ {
		username, createErr = utils.SecureRandomNumbers(11)
		if createErr != nil {
			log.Printf("generate registration username: %v", createErr)
			return "", "", code.CodeServerBusy
		}
		if userInformation, createErr = user.Register(username, email, password); createErr == nil {
			break
		}
		if exists, _, err := user.EmailExists(email); err == nil && exists {
			return "", "", code.CodeUserExist
		} else if err != nil {
			return "", "", code.CodeServerBusy
		}
		if !user.IsDuplicateEntry(createErr) {
			return "", "", code.CodeServerBusy
		}
	}
	if createErr != nil || userInformation == nil {
		return "", "", code.CodeServerBusy
	}

	// 6:生成Token
	token, err := myjwt.GenerateToken(userInformation.ID, userInformation.Username)

	if err != nil {
		log.Printf("generate token for registered user %d: %v", userInformation.ID, err)
		NotifyRegistrationEmailWorker()
		return "", username, code.CodeSuccess
	}

	NotifyRegistrationEmailWorker()
	return token, username, code.CodeSuccess
}

// 往指定邮箱发送验证码
// 分为以下任务：
// 1：先存放redis
// 2：再进行远程发送
func SendCaptcha(ctx context.Context, email_ string) code.Code {
	email_ = strings.ToLower(strings.TrimSpace(email_))
	sendCode, err := utils.SecureRandomNumbers(6)
	if err != nil {
		log.Printf("generate registration captcha: %v", err)
		return code.CodeServerBusy
	}
	//1:先存放到redis
	if err := myredis.SetCaptchaForEmail(ctx, email_, sendCode); err != nil {
		return code.CodeServerBusy
	}

	//2:再进行远程发送
	if err := myemail.SendCaptchaContext(ctx, email_, sendCode, myemail.CodeMsg); err != nil {
		if deleteErr := myredis.DeleteCaptchaIfMatch(ctx, email_, sendCode); deleteErr != nil {
			log.Printf("delete undelivered captcha for %s: %v", identifierForLog(email_), deleteErr)
		}
		return code.CodeServerBusy
	}

	return code.CodeSuccess
}

func identifierForLog(value string) string {
	value = strings.TrimSpace(value)
	if at := strings.LastIndex(value, "@"); at > 1 {
		return value[:1] + "***" + value[at:]
	}
	return "[redacted]"
}

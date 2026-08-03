package user

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	CodeMsg     = "GopherAI验证码如下(验证码仅限于2分钟有效): "
	UserNameMsg = "GopherAI的账号如下，请保留好，后续可以用账号进行登录 "
)

// 这边只能通过账号进行登录
func IsExistUser(username string) (bool, *model.User) {
	user, err := FindUserByUsername(username)

	if err == gorm.ErrRecordNotFound || user == nil {
		return false, nil
	}

	return true, user
}

func FindUserByUsername(username string) (*model.User, error) {
	return mysql.GetUserByUsername(strings.TrimSpace(username))
}

func IsExistEmail(email string) (bool, *model.User) {
	exists, user, _ := EmailExists(email)
	return exists, user
}

func EmailExists(email string) (bool, *model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := mysql.GetUserByEmail(email)
	if err == gorm.ErrRecordNotFound || user == nil {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, user, nil
}

func Register(username, email, password string) (*model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return mysql.InsertUser(&model.User{
		Email:                    email,
		Name:                     username,
		Username:                 username,
		Password:                 string(passwordHash),
		RegistrationEmailPending: true,
	})
}

func IsDuplicateEntry(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func ListPendingRegistrationEmails(ctx context.Context, limit int) ([]model.User, error) {
	if mysql.DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now()
	var users []model.User
	err := mysql.DB.WithContext(ctx).
		Where("registration_email_pending = ?", true).
		Where("registration_email_next_attempt IS NULL OR registration_email_next_attempt <= ?", now).
		Order("id ASC").
		Limit(limit).
		Find(&users).Error
	return users, err
}

func MarkRegistrationEmailSent(ctx context.Context, userID int64) error {
	if mysql.DB == nil {
		return errors.New("mysql is not initialized")
	}
	now := time.Now()
	return mysql.DB.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND registration_email_pending = ?", userID, true).
		Updates(map[string]interface{}{
			"registration_email_pending":      false,
			"registration_email_sent_at":      now,
			"registration_email_next_attempt": nil,
		}).Error
}

func MarkRegistrationEmailFailed(ctx context.Context, userID int64, nextAttempt time.Time) error {
	if mysql.DB == nil {
		return errors.New("mysql is not initialized")
	}
	return mysql.DB.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND registration_email_pending = ?", userID, true).
		Updates(map[string]interface{}{
			"registration_email_attempts":     gorm.Expr("registration_email_attempts + 1"),
			"registration_email_next_attempt": nextAttempt,
		}).Error
}

func UpgradePasswordHash(userID int64, password string) error {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return mysql.UpdateUserPassword(userID, string(passwordHash))
}

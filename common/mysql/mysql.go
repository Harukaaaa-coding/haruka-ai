package mysql

import (
	"GopherAI/config"
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitMysql() error {
	db, closeDB, err := Open()
	if err != nil {
		return err
	}
	if err := runStartupMigrationPolicy(db); err != nil {
		_ = closeDB()
		return err
	}
	DB = db
	return nil
}

// Open creates a configured database handle without changing global state and
// without applying schema migrations. Deployment tooling uses this entrypoint
// so migrations are an explicit release step rather than a side effect of API
// startup.
func Open() (*gorm.DB, func() error, error) {
	host := config.GetConfig().MysqlHost
	port := config.GetConfig().MysqlPort
	dbname := config.GetConfig().MysqlDatabaseName
	username := config.GetConfig().MysqlUser
	password := config.GetConfig().MysqlPassword
	charset := config.GetConfig().MysqlCharset

	//dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=%s&parseTime=true&loc=Local", username, password, host, port, dbname, charset)
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=true&loc=Local", username, password, host, port, dbname, charset)

	logLevel := logger.Warn
	if gin.Mode() == "debug" {
		logLevel = logger.Info
	}
	databaseLogger := logger.New(
		stdlog.New(os.Stdout, "\r\n", stdlog.LstdFlags),
		logger.Config{
			SlowThreshold:        200 * time.Millisecond,
			LogLevel:             logLevel,
			ParameterizedQueries: true,
			Colorful:             true,
		},
	)

	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       dsn,
		DefaultStringSize:         256,
		DisableDatetimePrecision:  true,
		DontSupportRenameIndex:    true,
		DontSupportRenameColumn:   true,
		SkipInitializeWithVersion: false,
	}), &gorm.Config{
		// Agent checkpoints and tool payloads are intentionally stored in
		// private columns. Parameterized SQL keeps those values out of debug
		// logs while preserving query timing and error diagnostics.
		Logger: databaseLogger,
	})
	if err != nil {
		return nil, nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)
	return db, sqlDB.Close, nil
}

func Ping(ctx context.Context) error {
	if DB == nil {
		return errors.New("mysql is not initialized")
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func Close() error {
	if DB == nil {
		return nil
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	DB = nil
	return err
}

func InsertUser(user *model.User) (*model.User, error) {
	if DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	err := DB.Create(user).Error
	return user, err
}

func GetUserByUsername(username string) (*model.User, error) {
	if DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	user := new(model.User)
	err := DB.Where("username = ?", username).First(user).Error
	return user, err
}

func GetUserByEmail(email string) (*model.User, error) {
	if DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	user := new(model.User)
	err := DB.Where("email = ?", email).First(user).Error
	return user, err
}

func UpdateUserPassword(userID int64, passwordHash string) error {
	if DB == nil {
		return errors.New("mysql is not initialized")
	}
	return DB.Model(&model.User{}).Where("id = ?", userID).Update("password", passwordHash).Error
}

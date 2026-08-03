package rag

import (
	"GopherAI/common/mysql"

	"gorm.io/gorm"
)

func ragDatabase() *gorm.DB {
	return mysql.DB
}

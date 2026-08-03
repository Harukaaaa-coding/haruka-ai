package redis

import (
	"GopherAI/config"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	redisCli "github.com/redis/go-redis/v9"
)

var Rdb *redisCli.Client

func Init(ctx context.Context) error {
	conf := config.GetConfig()
	host := conf.RedisConfig.RedisHost
	port := conf.RedisConfig.RedisPort
	password := conf.RedisConfig.RedisPassword
	db := conf.RedisDb
	addr := host + ":" + strconv.Itoa(port)

	Rdb = redisCli.NewClient(&redisCli.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
		Protocol: 2, // 使用 Protocol 2 避免 maint_notifications 警告
	})
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := Rdb.Ping(pingCtx).Err(); err != nil {
		_ = Rdb.Close()
		Rdb = nil
		return fmt.Errorf("connect to redis at %s: %w", addr, err)
	}
	return nil
}

func Close() error {
	if Rdb == nil {
		return nil
	}
	err := Rdb.Close()
	Rdb = nil
	return err
}

func Ping(ctx context.Context) error {
	if Rdb == nil {
		return fmt.Errorf("redis is not initialized")
	}
	return Rdb.Ping(ctx).Err()
}

func SetCaptchaForEmail(ctx context.Context, email, captcha string) error {
	if Rdb == nil {
		return fmt.Errorf("redis is not initialized")
	}
	key := GenerateCaptcha(email)
	expire := 2 * time.Minute
	return Rdb.Set(ctx, key, captcha, expire).Err()
}

const consumeCaptchaScript = `
local stored = redis.call('GET', KEYS[1])
if not stored then return 0 end
if string.lower(stored) ~= string.lower(ARGV[1]) then return 0 end
redis.call('DEL', KEYS[1])
return 1
`

func CheckCaptchaForEmail(ctx context.Context, email, userInput string) (bool, error) {
	if Rdb == nil {
		return false, fmt.Errorf("redis is not initialized")
	}
	key := GenerateCaptcha(email)
	result, err := Rdb.Eval(ctx, consumeCaptchaScript, []string{key}, userInput).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

const deleteCaptchaIfMatchScript = `
local stored = redis.call('GET', KEYS[1])
if stored and stored == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

func DeleteCaptchaIfMatch(ctx context.Context, email, captcha string) error {
	if Rdb == nil {
		return fmt.Errorf("redis is not initialized")
	}
	return Rdb.Eval(ctx, deleteCaptchaIfMatchScript, []string{GenerateCaptcha(email)}, captcha).Err()
}

// InitRedisIndex 初始化 Redis 索引，支持按文件名区分
func InitRedisIndex(ctx context.Context, filename string, dimension int) error {
	indexName := GenerateIndexName(filename)

	// 检查索引是否存在
	_, err := Rdb.Do(ctx, "FT.INFO", indexName).Result()
	if err == nil {
		fmt.Println("索引已存在，跳过创建")
		return nil
	}

	// 如果索引不存在，创建新索引
	if !strings.Contains(err.Error(), "Unknown index name") {
		return fmt.Errorf("检查索引失败: %w", err)
	}

	fmt.Println("正在创建 Redis 索引...")

	prefix := GenerateIndexNamePrefix(filename)

	// 创建索引
	createArgs := []interface{}{
		"FT.CREATE", indexName,
		"ON", "HASH",
		"PREFIX", "1", prefix,
		"SCHEMA",
		"content", "TEXT",
		"metadata", "TEXT",
		"vector", "VECTOR", "FLAT",
		"6",
		"TYPE", "FLOAT32",
		"DIM", dimension,
		"DISTANCE_METRIC", "COSINE",
	}

	if err := Rdb.Do(ctx, createArgs...).Err(); err != nil {
		return fmt.Errorf("创建索引失败: %w", err)
	}

	fmt.Println("索引创建成功！")
	return nil
}

// DeleteRedisIndex 删除 Redis 索引，支持按文件名区分
func DeleteRedisIndex(ctx context.Context, filename string) error {
	indexName := GenerateIndexName(filename)

	// 删除索引
	if err := Rdb.Do(ctx, "FT.DROPINDEX", indexName).Err(); err != nil {
		return fmt.Errorf("删除索引失败: %w", err)
	}

	fmt.Println("索引删除成功！")
	return nil
}

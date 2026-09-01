// Package cache 提供 Cache-Aside 读取抽象：memory（默认，进程内 TTL map）与 redis（可选）。
//
// 未配置 Redis 时使用 memory 兜底，零额外告警；配置 driver=redis 且连接失败时
// 降级为 memory 并输出一次 Warn。所有实现必须并发安全。
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// DefaultJanitorInterval memory 缓存过期清理 goroutine 的默认扫描间隔。
const DefaultJanitorInterval = 5 * time.Minute

// DefaultTTL Set 未显式传 ttl 时的默认过期时间。
const DefaultTTL = 10 * time.Minute

// Cache Cache-Aside 读取抽象。所有方法必须并发安全；值以 []byte 存取，
// 调用方负责序列化/反序列化。
type Cache interface {
	// Get 返回缓存值；未命中或已过期返回 (nil, false)。
	Get(ctx context.Context, key string) ([]byte, bool)
	// Set 写入缓存；ttl <= 0 时使用实现方默认 TTL。
	Set(ctx context.Context, key string, val []byte, ttl time.Duration)
	// Delete 删除指定 key（不存在时静默）。
	Delete(ctx context.Context, key string)
}

// CacheConfig 缓存配置（挂 config.yaml 顶层 cache:）。
type CacheConfig struct {
	// Driver: memory（默认）| redis；其他值按 memory 处理。
	Driver string `yaml:"driver,omitempty" json:"driver,omitempty"`
	// RedisAddr redis 地址，如 127.0.0.1:6379。
	RedisAddr string `yaml:"redis_addr,omitempty" json:"redis_addr,omitempty"`
	// RedisPassword redis 密码（可选）。
	RedisPassword string `yaml:"redis_password,omitempty" json:"redis_password,omitempty"`
	// RedisDB redis 逻辑库编号（默认 0）。
	RedisDB int `yaml:"redis_db,omitempty" json:"redis_db,omitempty"`
	// DefaultTTLSeconds 默认 TTL 秒数；<=0 时用 DefaultTTL（10 分钟）。
	DefaultTTLSeconds int `yaml:"default_ttl_seconds,omitempty" json:"default_ttl_seconds,omitempty"`
}

// DefaultTTLFor 返回配置生效的默认 TTL。
func (c CacheConfig) DefaultTTLFor() time.Duration {
	if c.DefaultTTLSeconds > 0 {
		return time.Duration(c.DefaultTTLSeconds) * time.Second
	}
	return DefaultTTL
}

// cacheEntry memory 缓存条目（带过期时间）。
type cacheEntry struct {
	val       []byte
	expiresAt time.Time
}

// MemoryCache 进程内 TTL map 缓存，带定期清理 goroutine（ctx 取消时退出）。
type MemoryCache struct {
	mu              sync.RWMutex
	data            map[string]cacheEntry
	defaultTTL      time.Duration
	janitorInterval time.Duration
}

// NewMemoryCache 构造 MemoryCache 并启动清理 goroutine；janitorInterval <= 0 用默认 5 分钟。
func NewMemoryCache(ctx context.Context, janitorInterval time.Duration) *MemoryCache {
	if janitorInterval <= 0 {
		janitorInterval = DefaultJanitorInterval
	}
	m := &MemoryCache{
		data:            make(map[string]cacheEntry),
		defaultTTL:      DefaultTTL,
		janitorInterval: janitorInterval,
	}
	go m.janitor(ctx)
	return m
}

// janitor 定期删除过期条目；ctx 取消时退出。
func (m *MemoryCache) janitor(ctx context.Context) {
	ticker := time.NewTicker(m.janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			m.mu.Lock()
			for k, e := range m.data {
				if now.After(e.expiresAt) {
					delete(m.data, k)
				}
			}
			m.mu.Unlock()
		}
	}
}

// Get 实现 Cache。
func (m *MemoryCache) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.RLock()
	e, ok := m.data[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		// 惰性过期：读到已过期条目时顺手删除
		m.mu.Lock()
		// 双检：janitor 或其他 Get 可能已删除/覆盖
		if cur, ok := m.data[key]; ok && !cur.expiresAt.After(time.Now()) {
			delete(m.data, key)
		}
		m.mu.Unlock()
		return nil, false
	}
	return e.val, true
}

// Set 实现 Cache；ttl <= 0 用默认 TTL。
func (m *MemoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) {
	if ttl <= 0 {
		ttl = m.defaultTTL
	}
	m.mu.Lock()
	m.data[key] = cacheEntry{val: val, expiresAt: time.Now().Add(ttl)}
	m.mu.Unlock()
}

// Delete 实现 Cache。
func (m *MemoryCache) Delete(_ context.Context, key string) {
	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()
}

// RedisCache 基于 go-redis v9 的缓存实现。
type RedisCache struct {
	client     *redis.Client
	defaultTTL time.Duration
}

// NewRedisCache 构造 RedisCache 并 Ping 验证连通性；失败返回 error（由调用方决定降级）。
func NewRedisCache(ctx context.Context, addr, password string, db int, defaultTTL time.Duration) (*RedisCache, error) {
	if defaultTTL <= 0 {
		defaultTTL = DefaultTTL
	}
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis 连接失败 (%s): %w", addr, err)
	}
	return &RedisCache{client: client, defaultTTL: defaultTTL}, nil
}

// Get 实现 Cache。
func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false // redis.Nil（未命中）与其他错误一律视为未命中，读路径不因缓存故障而失败
	}
	return val, true
}

// Set 实现 Cache；ttl <= 0 用默认 TTL。
func (r *RedisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if ttl <= 0 {
		ttl = r.defaultTTL
	}
	_ = r.client.Set(ctx, key, val, ttl).Err()
}

// Delete 实现 Cache。
func (r *RedisCache) Delete(ctx context.Context, key string) {
	_ = r.client.Del(ctx, key).Err()
}

// Close 释放 Redis 连接（MemoryCache 无需关闭）。
func (r *RedisCache) Close() error {
	return r.client.Close()
}

// NewFromConfig 按 CacheConfig 构造 Cache：
//   - driver 为空/memory/未知值 → MemoryCache（默认，零告警）
//   - driver=redis 且构造成功 → RedisCache
//   - driver=redis 但连接失败 → 降级 MemoryCache + Warn 一次
func NewFromConfig(ctx context.Context, cfg CacheConfig, logger *zap.Logger) Cache {
	ttl := cfg.DefaultTTLFor()
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "redis" {
		rc, err := NewRedisCache(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, ttl)
		if err == nil {
			return rc
		}
		if logger != nil {
			logger.Warn("cache.driver=redis 连接失败，降级为 memory 缓存", zap.String("addr", cfg.RedisAddr), zap.Error(err))
		}
	}
	return NewMemoryCache(ctx, DefaultJanitorInterval)
}

// KeyHash 用 SHA-256 生成缓存 key（避免长文本/特殊字符直接做 key）。
func KeyHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(h[:])
}

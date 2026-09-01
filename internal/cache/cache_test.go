package cache

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestMemoryCacheGetSetDelete memory 实现 Set/Get/Delete 基本闭环
func TestMemoryCacheGetSetDelete(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(ctx, time.Second)
	if _, ok := c.Get(ctx, "missing"); ok {
		t.Fatal("不存在的 key 应返回 false")
	}
	c.Set(ctx, "k1", []byte("v1"), time.Minute)
	if v, ok := c.Get(ctx, "k1"); !ok || string(v) != "v1" {
		t.Fatalf("Get(k1)=%q ok=%v，期望 v1/true", v, ok)
	}
	c.Delete(ctx, "k1")
	if _, ok := c.Get(ctx, "k1"); ok {
		t.Fatal("Delete 后应取不到")
	}
}

// TestMemoryCacheTTLExpiry TTL 到期自动过期
func TestMemoryCacheTTLExpiry(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(ctx, 50*time.Millisecond)
	c.Set(ctx, "exp", []byte("v"), 80*time.Millisecond)
	if _, ok := c.Get(ctx, "exp"); !ok {
		t.Fatal("TTL 内应可取")
	}
	time.Sleep(120 * time.Millisecond)
	if _, ok := c.Get(ctx, "exp"); ok {
		t.Fatal("TTL 过期后应取不到")
	}
}

// TestMemoryCacheConcurrentSetGet 并发 Set/Get 无 race（-race 验证）
func TestMemoryCacheConcurrentSetGet(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(ctx, time.Second)
	done := make(chan struct{}, 4)
	for i := 0; i < 2; i++ {
		go func(n int) {
			for j := 0; j < 200; j++ {
				c.Set(ctx, "k", []byte("v"), time.Minute)
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 2; i++ {
		go func() {
			for j := 0; j < 200; j++ {
				_, _ = c.Get(ctx, "k")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
}

// TestNewFromConfigMemoryDriver driver=memory 返回 MemoryCache
func TestNewFromConfigMemoryDriver(t *testing.T) {
	ctx := context.Background()
	c := NewFromConfig(ctx, CacheConfig{Driver: "memory"}, zap.NewNop())
	if _, ok := c.(*MemoryCache); !ok {
		t.Fatalf("driver=memory 应返回 *MemoryCache，实际 %T", c)
	}
}

// TestNewFromConfigRedisDowngradesToMemory driver=redis 但地址不可达时降级 memory，不 panic
func TestNewFromConfigRedisDowngradesToMemory(t *testing.T) {
	ctx := context.Background()
	cfg := CacheConfig{
		Driver:         "redis",
		RedisAddr:      "127.0.0.1:1", // 不可达端口
		RedisPassword:  "",
		RedisDB:        0,
		DefaultTTLSeconds: 60,
	}
	c := NewFromConfig(ctx, cfg, zap.NewNop())
	if _, ok := c.(*MemoryCache); !ok {
		t.Fatalf("redis 不可达应降级为 *MemoryCache，实际 %T", c)
	}
	// 降级后仍可用
	c.Set(ctx, "after-degrade", []byte("v"), time.Minute)
	if v, ok := c.Get(ctx, "after-degrade"); !ok || string(v) != "v" {
		t.Fatalf("降级后 memory 仍应可用，got %q ok=%v", v, ok)
	}
}

// TestKeyHash 稳定且不同输入不同 hash
func TestKeyHash(t *testing.T) {
	h1 := KeyHash("a", "b")
	h2 := KeyHash("a", "b")
	h3 := KeyHash("a", "c")
	if h1 != h2 {
		t.Fatal("相同输入应得相同 hash")
	}
	if h1 == h3 {
		t.Fatal("不同输入应得不同 hash")
	}
	if len(h1) != 64 {
		t.Fatalf("期望 sha256 hex 64 字符，实际 %d", len(h1))
	}
}

package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// internalError 返回脱敏的 500 响应：完整错误进日志（带 location），客户端只见通用消息。
// 用于所有 5xx 分支，避免 err.Error() 把内部实现细节（SQL、路径、上游地址等）泄露给客户端。
func internalError(c *gin.Context, logger *zap.Logger, location string, err error) {
	if logger != nil {
		logger.Error("internal error", zap.String("at", location), zap.Error(err))
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
}

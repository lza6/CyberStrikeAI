package blackboard

import "errors"

// 板级错误。用 sentinel 便于测试断言。
var (
	errEmptyOldID  = errors.New("blackboard: supersede oldID is empty")
	errOldNotFound = errors.New("blackboard: old finding not found")
)

// 这些导出别名让调用方/测试不依赖内部命名细节。
var (
	// ErrEmptyOldID supersede 时 oldID 为空。
	ErrEmptyOldID = errEmptyOldID
	// ErrOldNotFound supersede 时 old finding 不存在。
	ErrOldNotFound = errOldNotFound
)

// Package swarm — 文件 mailbox：每条消息一个 JSON 文件，原子写，跨平台 flock。
//
// 移植自 OpenHarness swarm/mailbox.py:102 TeammateMailbox。
// 路径：<HomeDir>/teams/<team>/agents/<agent_id>/inbox/<timestamp>_<message_id>.json
// 原子写：先写 .tmp 再 os.Rename（跨平台，无 CGO flock 依赖）。
// 并发保护：lockfile + O_EXCL 自旋获取（Windows/Unix 通用）。
package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Mailbox 消息类型常量。移植自 OpenHarness swarm/mailbox.py:27 MessageType。
const (
	MsgUserMessage        = "user_message"
	MsgPermissionRequest  = "permission_request"
	MsgPermissionResponse = "permission_response"
	MsgShutdown           = "shutdown"
	MsgIdleNotification   = "idle_notification"
)

// Mailbox 是单个 agent 在 swarm team 中的文件邮箱。
//
// 移植自 OpenHarness swarm/mailbox.py:102 TeammateMailbox。
type Mailbox struct {
	teamName string
	agentID  string
	homeDir  string // 统一 home（~/.cyberstrikeai/）
}

// validPathComponent 校验单一路径组件不引入目录穿越。
//
// 允许 [A-Za-z0-9._@-]（agentID 格式为 name@team，故允许 @），拒绝
// 路径分隔符、空段、. 和 ..。用于阻断 teamName/agentID 穿越 mailbait 目录。
func validPathComponent(s string) bool {
	if s == "" {
		return false
	}
	if s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return false
		}
	}
	return true
}

// NewMailbox 创建一个文件 mailbox。homeDir 为空时返回错误。
func NewMailbox(homeDir, teamName, agentID string) (*Mailbox, error) {
	if strings.TrimSpace(homeDir) == "" {
		return nil, errors.New("swarm: mailbox homeDir is empty")
	}
	if strings.TrimSpace(teamName) == "" || strings.TrimSpace(agentID) == "" {
		return nil, errors.New("swarm: mailbox teamName and agentID must be non-empty")
	}
	// H1 修复：净化 teamName/agentID，防目录穿越
	if !validPathComponent(teamName) {
		return nil, errors.New("swarm: mailbox teamName contains invalid path characters")
	}
	if !validPathComponent(agentID) {
		return nil, errors.New("swarm: mailbox agentID contains invalid path characters")
	}
	return &Mailbox{homeDir: homeDir, teamName: teamName, agentID: agentID}, nil
}

// Dir 返回 inbox 目录路径，必要时创建（0700）。
func (m *Mailbox) Dir() (string, error) {
	dir := filepath.Join(m.homeDir, "teams", m.teamName, "agents", m.agentID, "inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("swarm: mkdir mailbox inbox: %w", err)
	}
	return dir, nil
}

// Write 原子写入一条消息到 inbox。
//
// 移植自 OpenHarness swarm/mailbox.py:126 write。Go 改写：
//   - 无 async，直接同步 I/O（Go goroutine 天然并发，调用方可用 go 包裹）
//   - flock 改为 lockfile + O_EXCL 自旋（跨平台）
func (m *Mailbox) Write(ctx context.Context, msg MailboxMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if msg.ID == "" {
		msg.ID = uuid.NewString()
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = float64(time.Now().UnixNano()) / 1e9
	}
	dir, err := m.Dir()
	if err != nil {
		return err
	}
	filename := fmt.Sprintf("%.6f_%s.json", msg.Timestamp, msg.ID)
	finalPath := filepath.Join(dir, filename)
	tmpPath := finalPath + ".tmp"

	payload, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return fmt.Errorf("swarm: marshal mailbox message: %w", err)
	}

	return withLock(dir, func() error {
		if err := os.WriteFile(tmpPath, payload, 0o600); err != nil {
			return fmt.Errorf("swarm: write tmp mailbox: %w", err)
		}
		if err := os.Rename(tmpPath, finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("swarm: rename tmp mailbox: %w", err)
		}
		return nil
	})
}

// ReadAll 读取 inbox 消息，按时间戳升序。unreadOnly=true 只返回未读。
//
// 移植自 OpenHarness swarm/mailbox.py:153 read_all。跳过 . 开头和 .tmp 结尾的文件。
func (m *Mailbox) ReadAll(ctx context.Context, unreadOnly bool) ([]MailboxMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := m.Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("swarm: read mailbox dir: %w", err)
	}
	var msgs []MailboxMessage
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // 跳过损坏/并发写的文件
		}
		var msg MailboxMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // 跳过损坏 JSON
		}
		if unreadOnly && msg.Read {
			continue
		}
		msgs = append(msgs, msg)
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].Timestamp < msgs[j].Timestamp
	})
	return msgs, nil
}

// MarkRead 将 messageID 标记为已读（原地改写文件）。
//
// 移植自 OpenHarness swarm/mailbox.py:183 mark_read。
func (m *Mailbox) MarkRead(ctx context.Context, messageID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := m.Dir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("swarm: read mailbox dir: %w", err)
	}
	return withLock(dir, func() error {
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tmp") {
				continue
			}
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var msg MailboxMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.ID != messageID {
				continue
			}
			msg.Read = true
			payload, err := json.MarshalIndent(msg, "", "  ")
			if err != nil {
				continue
			}
			tmpPath := path + ".tmp"
			if err := os.WriteFile(tmpPath, payload, 0o600); err != nil {
				continue
			}
			if err := os.Rename(tmpPath, path); err != nil {
				_ = os.Remove(tmpPath)
				continue
			}
			return nil
		}
		return ErrAgentNotFound
	})
}

// Clear 清空 inbox 所有消息文件（不含 . 开头）。
//
// 移植自 OpenHarness swarm/mailbox.py:211 clear。
func (m *Mailbox) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := m.Dir()
	if err != nil {
		return err
	}
	return withLock(dir, func() error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
		return nil
	})
}

// NewUserMessage 创建一条 user_message（移植 mailbox.py:253 create_user_message）。
func NewUserMessage(sender, recipient, content string) MailboxMessage {
	return newMessage(MsgUserMessage, sender, recipient, map[string]interface{}{"content": content})
}

// NewShutdownRequest 创建一条 shutdown（移植 mailbox.py:258 create_shutdown_request）。
func NewShutdownRequest(sender, recipient string) MailboxMessage {
	return newMessage(MsgShutdown, sender, recipient, map[string]interface{}{})
}

// NewIdleNotification 创建一条 idle_notification（移植 mailbox.py:263）。
func NewIdleNotification(sender, recipient, summary string) MailboxMessage {
	return newMessage(MsgIdleNotification, sender, recipient, map[string]interface{}{"summary": summary})
}

// NewPermissionRequest 创建一条 permission_request（移植 mailbox.py:277）。
func NewPermissionRequest(sender, recipient string, req map[string]interface{}) MailboxMessage {
	payload := map[string]interface{}{
		"type":                   MsgPermissionRequest,
		"request_id":             req["request_id"],
		"agent_id":               req["agent_id"],
		"tool_name":              req["tool_name"],
		"tool_use_id":            req["tool_use_id"],
		"description":            req["description"],
		"input":                  req["input"],
		"permission_suggestions": req["permission_suggestions"],
	}
	return newMessage(MsgPermissionRequest, sender, recipient, payload)
}

// NewPermissionResponse 创建一条 permission_response（移植 mailbox.py:306）。
func NewPermissionResponse(sender, recipient string, resp map[string]interface{}) MailboxMessage {
	payload := map[string]interface{}{
		"type":               MsgPermissionResponse,
		"request_id":         resp["request_id"],
		"subtype":            resp["subtype"],
		"error":              resp["error"],
		"updated_input":      resp["updated_input"],
		"permission_updates": resp["permission_updates"],
	}
	return newMessage(MsgPermissionResponse, sender, recipient, payload)
}

func newMessage(msgType, sender, recipient string, payload map[string]interface{}) MailboxMessage {
	return MailboxMessage{
		ID:        uuid.NewString(),
		Type:      msgType,
		Sender:    sender,
		Recipient: recipient,
		Payload:   payload,
		Timestamp: float64(time.Now().UnixNano()) / 1e9,
	}
}

// withLock 用 lockfile + O_EXCL 自旋获取独占锁，跨平台（Windows/Unix 通用，无 CGO）。
//
// 移植自 OpenHarness swarm/lockfile.py:exclusive_file_lock。OpenHarness 用 fcntl.flock，
// Windows 上 fcntl 不可用，改用 O_EXCL lockfile 自旋（与 OpenHarness Windows 兼容路径一致）。
func withLock(dir string, fn func() error) error {
	lockPath := filepath.Join(dir, ".write_lock")
	const maxAttempts = 50
	const baseSleep = 10 * time.Millisecond
	for i := 0; i < maxAttempts; i++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
			_ = f.Close()
			defer func() { _ = os.Remove(lockPath) }()
			return fn()
		}
		if os.IsExist(err) {
			// 检测陈旧锁：超过 30s 视为可抢占（持有者可能崩溃）
			if info, stErr := os.Stat(lockPath); stErr == nil {
				if time.Since(info.ModTime()) > 30*time.Second {
					_ = os.Remove(lockPath)
					continue
				}
			}
			time.Sleep(baseSleep * time.Duration(i+1))
			continue
		}
		return fmt.Errorf("swarm: create lockfile: %w", err)
	}
	return errors.New("swarm: mailbox lock timeout")
}

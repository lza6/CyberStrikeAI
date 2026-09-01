package security

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/database"

	"github.com/google/uuid"
)

// Predefined errors for authentication operations.
var (
	ErrInvalidPassword = errors.New("invalid password")
)

// Session represents an authenticated user session.
type Session struct {
	Token            string
	ExpiresAt        time.Time
	UserID           string
	Username         string
	DisplayName      string
	Roles            []string
	Permissions      map[string]bool
	PermissionScopes map[string]string
	Scope            string
}

// AuthManager manages password-based authentication and session lifecycle.
type AuthManager struct {
	sessionDuration time.Duration
	db              *database.DB

	mu          sync.RWMutex
	sessions    map[string]Session
	maxSessions int // 会话数上限，超限时驱逐最早过期会话，防内存耗尽

	// localMode 为 true 时，所有请求以内置 admin 全权限身份执行，免登录免 RBAC。
	// 仅供桌面版/本地单机部署使用，暴露到公网前必须关闭。
	localMode bool
}

// defaultMaxSessions 默认会话数上限。
const defaultMaxSessions = 1000

// NewAuthManager creates a new AuthManager instance.
func NewAuthManager(sessionDurationHours int) *AuthManager {
	return NewAuthManagerWithLimit(sessionDurationHours, defaultMaxSessions)
}

// NewAuthManagerWithLimit creates an AuthManager with an explicit session cap.
func NewAuthManagerWithLimit(sessionDurationHours, maxSessions int) *AuthManager {
	if sessionDurationHours <= 0 {
		sessionDurationHours = 12
	}
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}

	return &AuthManager{
		sessionDuration: time.Duration(sessionDurationHours) * time.Hour,
		sessions:        make(map[string]Session),
		maxSessions:     maxSessions,
	}
}

// SetLocalMode 启用或关闭本地免登录模式。
func (a *AuthManager) SetLocalMode(enabled bool) {
	a.mu.Lock()
	a.localMode = enabled
	a.mu.Unlock()
}

// IsLocalMode 返回当前是否处于本地免登录模式。
func (a *AuthManager) IsLocalMode() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.localMode
}

// LocalSession 返回 localMode 下使用的内置 admin 全权限会话。
// 该会话不落盘到 sessions map（每个请求临时构造），也不可被 Revoke。
func (a *AuthManager) LocalSession() Session {
	perms := make(map[string]bool, len(PermissionCatalog))
	for p := range PermissionCatalog {
		perms[p] = true
	}
	scopes := make(map[string]string, len(PermissionCatalog))
	for p := range PermissionCatalog {
		scopes[p] = database.RBACScopeAll
	}
	return Session{
		Token:            "local-mode",
		ExpiresAt:        time.Now().Add(a.sessionDuration),
		UserID:           "local-admin",
		Username:         "admin",
		DisplayName:      "Local Admin",
		Roles:            []string{"admin"},
		Permissions:      perms,
		PermissionScopes: scopes,
		Scope:            database.RBACScopeAll,
	}
}

// AttachRBACStore enables multi-user RBAC authentication. When no users exist yet,
// it bootstraps the built-in admin account and returns the generated initial password.
func (a *AuthManager) AttachRBACStore(db *database.DB) (generatedAdminPassword string, err error) {
	if db == nil {
		return "", errors.New("database is required for authentication")
	}

	needsAdminPassword, err := db.RBACNeedsAdminPassword()
	if err != nil {
		return "", err
	}

	adminPasswordHash := ""
	if needsAdminPassword {
		generatedAdminPassword, err = GenerateStrongPassword(24)
		if err != nil {
			return "", err
		}
		adminPasswordHash, err = HashPassword(generatedAdminPassword)
		if err != nil {
			return "", err
		}
	}

	if err := db.BootstrapRBAC(adminPasswordHash, PermissionCatalog); err != nil {
		return "", err
	}

	a.mu.Lock()
	a.db = db
	a.mu.Unlock()
	return generatedAdminPassword, nil
}

// Authenticate validates the password and creates a new session.
func (a *AuthManager) Authenticate(username, password string) (string, time.Time, error) {
	session, err := a.authenticateSession(username, password)
	if err != nil {
		return "", time.Time{}, err
	}
	a.mu.Lock()
	a.evictSessionLocked()
	a.sessions[session.Token] = session
	a.mu.Unlock()
	return session.Token, session.ExpiresAt, nil
}

// evictSessionLocked 在会话数达到上限时驱逐一条会话，为新会话腾位。
// 调用方必须已持有 a.mu 写锁。策略：先清已过期会话；没有过期会话时
// 驱逐 ExpiresAt 最早（最先到期）的一条。每次最多驱逐一条，保证 map 长度不超过 maxSessions。
func (a *AuthManager) evictSessionLocked() {
	if len(a.sessions) < a.maxSessions {
		return
	}
	now := time.Now()
	// 第一遍：清理全部已过期会话
	evicted := false
	for token, session := range a.sessions {
		if now.After(session.ExpiresAt) {
			delete(a.sessions, token)
			evicted = true
		}
	}
	if evicted || len(a.sessions) < a.maxSessions {
		return
	}
	// 第二遍：没有过期会话，驱逐最早到期的
	var earliestToken string
	var earliest time.Time
	for token, session := range a.sessions {
		if earliestToken == "" || session.ExpiresAt.Before(earliest) {
			earliestToken = token
			earliest = session.ExpiresAt
		}
	}
	if earliestToken != "" {
		delete(a.sessions, earliestToken)
	}
}

func (a *AuthManager) authenticateSession(username, password string) (Session, error) {
	token := uuid.NewString()
	expiresAt := time.Now().Add(a.sessionDuration)

	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		return Session{}, errors.New("authentication store is not configured")
	}

	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		username = "admin"
	}
	user, err := db.GetRBACUserByUsername(username)
	if err != nil {
		if err == sql.ErrNoRows {
			return Session{}, ErrInvalidPassword
		}
		return Session{}, err
	}
	if !user.Enabled || !VerifyPasswordHash(password, user.PasswordHash) {
		return Session{}, ErrInvalidPassword
	}
	access, err := db.ResolveRBACAccess(user.ID)
	if err != nil {
		return Session{}, err
	}
	roleIDs := make([]string, 0, len(access.Roles))
	for _, role := range access.Roles {
		roleIDs = append(roleIDs, role.ID)
	}
	return Session{
		Token:            token,
		ExpiresAt:        expiresAt,
		UserID:           user.ID,
		Username:         user.Username,
		DisplayName:      user.DisplayName,
		Roles:            roleIDs,
		Permissions:      access.Permissions,
		PermissionScopes: access.PermissionScopes,
		Scope:            access.Scope,
	}, nil
}

func (s Session) ScopeFor(permission string) string {
	if scope := strings.TrimSpace(s.PermissionScopes[strings.TrimSpace(permission)]); scope != "" {
		return scope
	}
	return strings.TrimSpace(s.Scope)
}

// ValidateToken checks whether the provided token is still valid.
func (a *AuthManager) ValidateToken(token string) (Session, bool) {
	if strings.TrimSpace(token) == "" {
		return Session{}, false
	}

	a.mu.RLock()
	session, ok := a.sessions[token]
	a.mu.RUnlock()
	if !ok {
		return Session{}, false
	}

	if time.Now().After(session.ExpiresAt) {
		a.mu.Lock()
		delete(a.sessions, token)
		a.mu.Unlock()
		return Session{}, false
	}

	return session, true
}

// CheckPassword verifies whether the provided password matches the current password.
func (a *AuthManager) CheckPassword(password string) bool {
	return a.CheckUserPassword("admin", password)
}

// CheckUserPassword verifies whether the provided password matches a user.
func (a *AuthManager) CheckUserPassword(username, password string) bool {
	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		return false
	}
	user, err := db.GetRBACUserByUsername(username)
	if err != nil {
		return false
	}
	return VerifyPasswordHash(password, user.PasswordHash)
}

func (a *AuthManager) UpdateUserPassword(userID, password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return errors.New("auth password must be configured")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		return errors.New("authentication store is not configured")
	}
	if err := db.UpdateRBACUserPassword(userID, hash); err != nil {
		return err
	}
	a.mu.Lock()
	for token, session := range a.sessions {
		if session.UserID == userID {
			delete(a.sessions, token)
		}
	}
	a.mu.Unlock()
	return nil
}

// RevokeToken invalidates the specified token.
func (a *AuthManager) RevokeToken(token string) {
	if strings.TrimSpace(token) == "" {
		return
	}

	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

func (a *AuthManager) RevokeUserSessions(userID string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	a.mu.Lock()
	for token, session := range a.sessions {
		if session.UserID == userID {
			delete(a.sessions, token)
		}
	}
	a.mu.Unlock()
}

func (a *AuthManager) RevokeAllSessions() {
	a.mu.Lock()
	a.sessions = make(map[string]Session)
	a.mu.Unlock()
}

// SessionDurationHours returns the configured session duration in hours.
func (a *AuthManager) SessionDurationHours() int {
	return int(a.sessionDuration / time.Hour)
}

func allPermissions() map[string]bool {
	out := make(map[string]bool, len(PermissionCatalog))
	for key := range PermissionCatalog {
		out[key] = true
	}
	return out
}

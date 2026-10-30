// auth_session.go —— 登录态持久化与静默续期
//
// 目标：① 重启应用不必再走一遍浏览器授权；② access_token 快过期时用 refresh_token
// 自动续期，长时间挂着也不会突然 401。
//
// 落盘位置：<配置目录>/CHMLFrp-SpeedTest/session.bin（整体加密后的 base64 文本）。
// Windows 用 DPAPI 加密（绑定当前用户，拷到别的机器/别的账户解不开）；
// 其他平台退化为 0600 明文——和 gh CLI / VS Code 的做法一致。
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sessionFileVersion 文件头。带版本号，将来换格式可以直接判废。
const sessionFileVersion = "CHMLS1:"

// authSession 落盘的登录态。整体加密，结构体本身不会以明文出现。
type authSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix 秒，0 表示未知
	Username     string `json:"username"`
	Email        string `json:"email"`
	Picture      string `json:"picture"`
}

// SessionState 给前端的登录态快照。注意不含任何令牌。
type SessionState struct {
	LoggedIn bool   `json:"loggedIn"`
	Remember bool   `json:"remember"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Picture  string `json:"picture"`
}

func sessionFilePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "", fmt.Errorf("无法定位配置目录: %w", err)
	}
	dir := filepath.Join(base, "CHMLFrp-SpeedTest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建配置目录失败: %w", err)
	}
	return filepath.Join(dir, "session.bin"), nil
}

// saveSession 加密后原子落盘（先写 .tmp 再 rename，避免断电留半个文件）。
func saveSession(s authSession) error {
	path, err := sessionFilePath()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	sealed, err := sealBytes(raw)
	if err != nil {
		return fmt.Errorf("加密登录态失败: %w", err)
	}
	payload := sessionFileVersion + base64.StdEncoding.EncodeToString(sealed)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(payload), 0o600); err != nil {
		return fmt.Errorf("写入登录态失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存登录态失败: %w", err)
	}
	return nil
}

// loadSession 读取并解密登录态。文件不存在时返回 os.ErrNotExist 包装的错误。
func loadSession() (authSession, error) {
	var s authSession
	path, err := sessionFilePath()
	if err != nil {
		return s, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, sessionFileVersion) {
		return s, fmt.Errorf("登录态文件格式不可识别")
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(text, sessionFileVersion))
	if err != nil {
		return s, fmt.Errorf("登录态文件已损坏: %w", err)
	}
	plain, err := openBytes(sealed)
	if err != nil {
		return s, fmt.Errorf("解密登录态失败: %w", err)
	}
	if err := json.Unmarshal(plain, &s); err != nil {
		return s, fmt.Errorf("解析登录态失败: %w", err)
	}
	return s, nil
}

// removeSessionFile 删除本地登录态（退出登录 / 关闭"记住登录" / 续期失败时调用）。
func removeSessionFile() {
	if path, err := sessionFilePath(); err == nil {
		_ = os.Remove(path)
	}
}

// tokenExpiryFrom 把 access_token 的 expires_in 换算成绝对过期时间。
func tokenExpiryFrom(expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = 3600 // 服务端没给就按 1 小时保守处理
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second)
}

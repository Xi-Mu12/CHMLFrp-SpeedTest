package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	qzhuaIssuer = "https://account-api.qzhua.net"
	qzhuaScope  = "openid profile email offline_access"
	// client_id 不是机密信息（Public Client 无 client_secret），直接内置，
	// 终端用户无需任何配置。仅开发时可用环境变量 QZHUA_CLIENT_ID 覆盖。
	qzhuaClientID = "01a171b12d7c732cae3f21bd6a10963a"
)

type qzhuaOIDCConfig struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

type qzhuaToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type qzhuaUser struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Picture  string `json:"picture"`
}

type qzhuaAuthEvent struct {
	Status    string `json:"status"`
	Username  string `json:"username,omitempty"`
	Email     string `json:"email,omitempty"`
	Picture   string `json:"picture,omitempty"`
	Error     string `json:"error,omitempty"`
	VerifyURL string `json:"verifyUrl,omitempty"`
}

type QZhuaLoginStart struct {
	VerifyURL string `json:"verifyUrl"`
}

func qzhuaClientIDValue() string {
	if value := strings.TrimSpace(os.Getenv("QZHUA_CLIENT_ID")); value != "" {
		return value
	}
	return qzhuaClientID
}

func qzhuaRedirectURI() string {
	if value := strings.TrimSpace(os.Getenv("QZHUA_REDIRECT_URI")); value != "" {
		return value
	}
	return "http://127.0.0.1:49152/oauth/callback"
}

func randomURLValue(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (a *App) emitAuth(event qzhuaAuthEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "qzhuaAuth", event)
	}
}

func fetchQZhuaOIDC(client *http.Client) (qzhuaOIDCConfig, error) {
	var config qzhuaOIDCConfig
	resp, err := client.Get(strings.TrimRight(qzhuaIssuer, "/") + "/.well-known/openid-configuration")
	if err != nil {
		return config, fmt.Errorf("读取认证配置失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if resp.StatusCode/100 != 2 {
		return config, fmt.Errorf("读取认证配置失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return config, fmt.Errorf("解析认证配置失败: %w", err)
	}
	if config.AuthorizationEndpoint == "" || config.TokenEndpoint == "" || config.UserinfoEndpoint == "" {
		return config, fmt.Errorf("认证配置缺少必要端点")
	}
	return config, nil
}

// StartQZhuaLogin 启动桌面端 Authorization Code + PKCE 登录流程。
// 应用不使用也不保存 client_secret。
func (a *App) StartQZhuaLogin() (QZhuaLoginStart, error) {
	clientID := qzhuaClientIDValue()
	if clientID == "" || clientID == qzhuaClientID {
		return QZhuaLoginStart{}, fmt.Errorf("未配置 QZhua client_id，请设置 QZHUA_CLIENT_ID")
	}
	redirectURI := qzhuaRedirectURI()
	redirect, err := url.Parse(redirectURI)
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" {
		return QZhuaLoginStart{}, fmt.Errorf("桌面端回调地址必须使用 127.0.0.1 HTTP 地址")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	config, err := fetchQZhuaOIDC(client)
	if err != nil {
		return QZhuaLoginStart{}, err
	}
	state, err := randomURLValue(32)
	if err != nil {
		return QZhuaLoginStart{}, fmt.Errorf("生成登录状态失败: %w", err)
	}
	verifier, err := randomURLValue(48)
	if err != nil {
		return QZhuaLoginStart{}, fmt.Errorf("生成 PKCE 校验值失败: %w", err)
	}

	listener, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return QZhuaLoginStart{}, fmt.Errorf("启动登录回调失败: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.authMu.Lock()
	if a.authCancel != nil {
		a.authCancel()
	}
	a.authCancel = func() {
		cancel()
		listener.Close()
	}
	a.authMu.Unlock()

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {qzhuaScope},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	authURL := config.AuthorizationEndpoint + "?" + params.Encode()
	a.emitAuth(qzhuaAuthEvent{Status: "waiting", VerifyURL: authURL})
	runtime.BrowserOpenURL(a.ctx, authURL)
	go a.handleQZhuaCallback(ctx, listener, client, config, clientID, redirectURI, redirect.Path, state, verifier)
	return QZhuaLoginStart{VerifyURL: authURL}, nil
}

func (a *App) CancelQZhuaLogin() {
	a.authMu.Lock()
	if a.authCancel != nil {
		a.authCancel()
		a.authCancel = nil
	}
	a.authMu.Unlock()
	a.emitAuth(qzhuaAuthEvent{Status: "cancelled"})
}

func (a *App) handleQZhuaCallback(ctx context.Context, listener net.Listener, client *http.Client, config qzhuaOIDCConfig, clientID, redirectURI, callbackPath, state, verifier string) {
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != callbackPath {
			http.NotFound(w, req)
			return
		}
		if req.URL.Query().Get("state") != state {
			http.Error(w, "登录状态校验失败，请关闭此页面后重试。", http.StatusBadRequest)
			a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "登录状态校验失败"})
			return
		}
		if callbackError := req.URL.Query().Get("error"); callbackError != "" {
			http.Error(w, "授权未完成，请返回应用重试。", http.StatusBadRequest)
			a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "授权未完成: " + callbackError})
			return
		}
		code := req.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "缺少授权码，请返回应用重试。", http.StatusBadRequest)
			a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "回调缺少授权码"})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html><body><p>登录完成，可以返回应用。</p></body></html>")
		go a.finishQZhuaLogin(ctx, client, config, clientID, redirectURI, code, verifier)
	})}
	go server.Serve(listener)
	<-ctx.Done()
	server.Close()
}

func (a *App) finishQZhuaLogin(ctx context.Context, client *http.Client, config qzhuaOIDCConfig, clientID, redirectURI, code, verifier string) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "创建令牌请求失败: " + err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "换取令牌失败: " + err.Error()})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: fmt.Sprintf("换取令牌失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))})
		return
	}
	var token qzhuaToken
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "令牌响应缺少 access_token"})
		return
	}
	user, err := fetchQZhuaUser(client, config.UserinfoEndpoint, token.AccessToken)
	if err != nil {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: err.Error()})
		return
	}
	a.authMu.Lock()
	a.qzhuaUser = user
	a.authMu.Unlock()
	a.emitAuth(qzhuaAuthEvent{Status: "logged_in", Username: user.Username, Email: user.Email, Picture: user.Picture})
}

func fetchQZhuaUser(client *http.Client, endpoint, accessToken string) (qzhuaUser, error) {
	var user qzhuaUser
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return user, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return user, fmt.Errorf("获取用户信息失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode/100 != 2 {
		return user, fmt.Errorf("获取用户信息失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return user, fmt.Errorf("解析用户信息失败: %w", err)
	}
	return user, nil
}

func (a *App) LogoutQZhua() {
	a.authMu.Lock()
	if a.authCancel != nil {
		a.authCancel()
		a.authCancel = nil
	}
	a.qzhuaUser = qzhuaUser{}
	a.authMu.Unlock()
	a.emitAuth(qzhuaAuthEvent{Status: "logged_out"})
}

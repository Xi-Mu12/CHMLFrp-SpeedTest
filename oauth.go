package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	qzhuaIssuer = "https://account-api.qzhua.net"
	// 设备码授权端点的 scope。
	// 实测（2026-10-25，本应用 client_id 01a171b1…）各 scope 在该端点的结果：
	//   openid / email / 任何包含它们的组合 → 400 invalid_scope
	//   profile / offline_access / chmlfrp_api / 三者组合 → 200
	//   不带 scope → 200（服务端默认）
	// 注意：授权码流程用 openid 正常，设备码端点却拒绝它，服务端两处校验不一致。
	// 官方启动器用自己的 client_id（019d4334…）+ "profile email offline_access chmlfrp_api"。
	// 这里取 profile + offline_access + chmlfrp_api：profile 用于 OIDC 资料，
	// offline_access 换 refresh_token，chmlfrp_api 访问 CHMLFrp 受保护接口；
	// 邮箱从 CHMLFrp 的 /userinfo 拿，不依赖被拒的 email scope。
	qzhuaDeviceScope = "profile offline_access chmlfrp_api"
	// client_id 不是机密信息（Public Client 无 client_secret），可以随桌面应用发布。
	// 环境变量 QZHUA_CLIENT_ID 仍可用于开发和替换客户端配置。
	qzhuaClientID = "01a171b12d7c732cae3f21bd6a10963a"
	// RFC 8628 设备码授权流程的 grant 类型。
	qzhuaDeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	// 服务端未返回 interval / expires_in 时的兜底值（秒）。
	defaultDevicePollInterval = 5
	maxDevicePollInterval     = 30
	defaultDeviceExpiry       = 600
)

type qzhuaOIDCConfig struct {
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	UserinfoEndpoint            string `json:"userinfo_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

type qzhuaToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// qzhuaDeviceAuth 设备码授权端点返回的设备码信息（RFC 8628 §3.2）。
type qzhuaDeviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type qzhuaUser struct {
	Sub               string `json:"sub"`
	Username          string `json:"username"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Name              string `json:"name,omitempty"`
	Nickname          string `json:"nickname,omitempty"`
	Email             string `json:"email"`
	Picture           string `json:"picture"`
	Avatar            string `json:"avatar,omitempty"`
}

// AuthProfile 汇总 OAuth/OIDC 用户资料和 CHMLFrp 用户资料。
type AuthProfile struct {
	QZhua qzhuaUser `json:"qzhua"`
	Chml  *ChmlUser `json:"chml,omitempty"`
}

type qzhuaAuthEvent struct {
	Status    string       `json:"status"`
	Username  string       `json:"username,omitempty"`
	Email     string       `json:"email,omitempty"`
	Picture   string       `json:"picture,omitempty"`
	Error     string       `json:"error,omitempty"`
	APIError  string       `json:"apiError,omitempty"`
	Profile   *AuthProfile `json:"profile,omitempty"`
	VerifyURL string       `json:"verifyUrl,omitempty"`
	UserCode  string       `json:"userCode,omitempty"`
	ExpiresIn int          `json:"expiresIn,omitempty"`
}

type QZhuaLoginStart struct {
	VerifyURL string `json:"verifyUrl"`
	UserCode  string `json:"userCode"`
	ExpiresIn int    `json:"expiresIn"`
}

func qzhuaClientIDValue() string {
	if value := strings.TrimSpace(os.Getenv("QZHUA_CLIENT_ID")); value != "" {
		return value
	}
	return qzhuaClientID
}

func (a *App) emitAuth(event qzhuaAuthEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "qzhuaAuth", event)
	}
}

// stopQZhuaAttempt 终止上一次登录尝试（取消轮询上下文）。
func (a *App) stopQZhuaAttempt() {
	a.authMu.Lock()
	cancel := a.authCancel
	a.authCancel = nil
	a.authMu.Unlock()
	if cancel != nil {
		cancel()
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
	if config.TokenEndpoint == "" || config.UserinfoEndpoint == "" {
		return config, fmt.Errorf("认证配置缺少必要端点")
	}
	if config.DeviceAuthorizationEndpoint == "" {
		return config, fmt.Errorf("认证服务器不支持设备码授权流程")
	}
	return config, nil
}

// StartQZhuaLogin 启动桌面端 OAuth 2.0 设备码授权流程（RFC 8628）。
// 应用不使用也不保存 client_secret；登录确认在用户浏览器中完成。
func (a *App) StartQZhuaLogin() (QZhuaLoginStart, error) {
	clientID := qzhuaClientIDValue()
	if clientID == "" || strings.EqualFold(clientID, "YOUR_QZHUA_CLIENT_ID") {
		return QZhuaLoginStart{}, fmt.Errorf("未配置 QZhua client_id，请设置 QZHUA_CLIENT_ID")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	config, err := fetchQZhuaOIDC(client)
	if err != nil {
		return QZhuaLoginStart{}, err
	}

	// 失败重试时，先取消上一次登录尝试。
	a.stopQZhuaAttempt()
	device, err := requestQZhuaDeviceCode(client, config, clientID)
	if err != nil {
		return QZhuaLoginStart{}, err
	}

	interval := device.Interval
	if interval <= 0 {
		interval = defaultDevicePollInterval
	}
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = defaultDeviceExpiry
	}
	// 优先使用 verification_uri_complete：已内嵌 user_code，用户无需手输。
	verifyURL := device.VerificationURIComplete
	if verifyURL == "" {
		verifyURL = device.VerificationURI
	}
	if verifyURL == "" {
		return QZhuaLoginStart{}, fmt.Errorf("设备码响应缺少验证网址")
	}

	// 设备码到期后自动停止轮询；用户取消时走 stopQZhuaAttempt。
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(expiresIn)*time.Second)
	a.authMu.Lock()
	a.authCancel = cancel
	a.authMu.Unlock()

	a.emitAuth(qzhuaAuthEvent{
		Status:    "waiting",
		VerifyURL: verifyURL,
		UserCode:  device.UserCode,
		ExpiresIn: expiresIn,
	})
	runtime.BrowserOpenURL(a.ctx, verifyURL)
	go a.pollQZhuaDeviceToken(ctx, cancel, client, config, clientID, device.DeviceCode, interval)
	return QZhuaLoginStart{VerifyURL: verifyURL, UserCode: device.UserCode, ExpiresIn: expiresIn}, nil
}

// requestQZhuaDeviceCode 向设备码授权端点申请 device_code 与 user_code。
func requestQZhuaDeviceCode(client *http.Client, config qzhuaOIDCConfig, clientID string) (qzhuaDeviceAuth, error) {
	var device qzhuaDeviceAuth
	form := url.Values{
		"client_id": {clientID},
		"scope":     {qzhuaDeviceScope},
	}
	resp, err := client.PostForm(config.DeviceAuthorizationEndpoint, form)
	if err != nil {
		return device, fmt.Errorf("申请设备码失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode/100 != 2 {
		return device, fmt.Errorf("申请设备码失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &device); err != nil {
		return device, fmt.Errorf("解析设备码响应失败: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" {
		return device, fmt.Errorf("设备码响应缺少 device_code 或 user_code")
	}
	return device, nil
}

func (a *App) CancelQZhuaLogin() {
	a.stopQZhuaAttempt()
	a.emitAuth(qzhuaAuthEvent{Status: "cancelled"})
}

// pollQZhuaDeviceToken 按 RFC 8628 轮询 token 端点，直到用户完成授权、
// 拒绝授权、设备码过期或上下文被取消。
// 与官方启动器一致：拿到设备码后立即发起首次轮询，之后按 interval 间隔重试。
func (a *App) pollQZhuaDeviceToken(ctx context.Context, cancel context.CancelFunc, client *http.Client, config qzhuaOIDCConfig, clientID, deviceCode string, interval int) {
	defer cancel()
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				a.emitDevicePollStop(ctx)
				return
			case <-time.After(time.Duration(interval) * time.Second):
			}
		}
		token, status, err := pollQZhuaDeviceTokenOnce(ctx, client, config, clientID, deviceCode)
		switch status {
		case "pending":
			continue
		case "slow_down":
			// 服务端要求放慢轮询，间隔递增（封顶 maxDevicePollInterval）。
			if interval < maxDevicePollInterval {
				interval += 5
			}
			continue
		}
		if err != nil {
			a.emitAuth(qzhuaAuthEvent{Status: "error", Error: err.Error()})
			return
		}
		a.completeQZhuaLogin(ctx, client, config, token)
		return
	}
}

// emitDevicePollStop 区分「用户取消」和「设备码到期」两种停止原因。
func (a *App) emitDevicePollStop(ctx context.Context) {
	if ctx.Err() == context.DeadlineExceeded {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "这次设备授权已过期，请重新开始登录。"})
		return
	}
	a.emitAuth(qzhuaAuthEvent{Status: "cancelled"})
}

// pollQZhuaDeviceTokenOnce 发起一次设备码换令牌请求。
// 返回 (令牌, 状态, 错误)：状态为 pending / slow_down 时错误为 nil，需继续轮询。
func pollQZhuaDeviceTokenOnce(ctx context.Context, client *http.Client, config qzhuaOIDCConfig, clientID, deviceCode string) (qzhuaToken, string, error) {
	var token qzhuaToken
	form := url.Values{
		"grant_type":  {qzhuaDeviceGrant},
		"device_code": {deviceCode},
		"client_id":   {clientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return token, "", fmt.Errorf("创建令牌请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// 网络抖动不算致命，按未授权处理继续轮询。
		return token, "pending", nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
			return token, "", fmt.Errorf("令牌响应缺少 access_token")
		}
		return token, "ok", nil
	}
	// RFC 8628：轮询期间的错误以 400 + error 字段返回。
	var oauthErr struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &oauthErr)
	switch oauthErr.Error {
	case "authorization_pending":
		return token, "pending", nil
	case "slow_down":
		return token, "slow_down", nil
	case "access_denied":
		return token, "", fmt.Errorf("你已取消本次授权，请重新开始登录")
	case "expired_token":
		return token, "", fmt.Errorf("这次设备授权已过期，请重新开始登录")
	default:
		msg := strings.TrimSpace(oauthErr.Description)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if msg == "" {
			msg = "未知错误"
		}
		return token, "", fmt.Errorf("换取令牌失败(%d): %s", resp.StatusCode, msg)
	}
}

// completeQZhuaLogin 用已取得的 access_token 拉取用户资料并完成登录。
func (a *App) completeQZhuaLogin(ctx context.Context, client *http.Client, config qzhuaOIDCConfig, token qzhuaToken) {
	// 设备码流程的令牌只带 chmlfrp_api scope，OIDC userinfo 可能因缺少 openid
	// 而拒绝访问。此时退化为只用 CHMLFrp 账户资料（官方启动器同样只取后者）。
	user, userErr := fetchQZhuaUser(ctx, client, config.UserinfoEndpoint, token.AccessToken)

	// OAuth 登录成功后，继续用同一个 access_token 获取 CHMLFrp 账户资料。
	// 即使该接口暂时不可用，也不应让已经完成的 OAuth 登录失败；前端会显示
	// OAuth 资料，并把 CHMLFrp 资料获取失败原因展示出来。
	chmlUser, chmlErr := FetchUserInfo(token.AccessToken)
	if userErr != nil && chmlErr != nil {
		a.emitAuth(qzhuaAuthEvent{Status: "error", Error: "获取账号资料失败: " + chmlErr.Error()})
		return
	}

	profile := &AuthProfile{QZhua: user}
	apiError := ""
	if chmlErr == nil {
		profile.Chml = &chmlUser
	} else {
		apiError = chmlErr.Error()
	}
	if userErr != nil {
		// OIDC 资料缺失时，用 CHMLFrp 资料补上展示所需的最小字段。
		// 这不影响登录结果，因此不写入 APIError（前端会优先展示 CHMLFrp 资料）。
		user.Username = chmlUser.Username
		user.Email = chmlUser.Email
		user.Picture = chmlUser.UserImage
	}

	a.authMu.Lock()
	a.qzhuaUser = user
	a.qzhuaToken = token.AccessToken
	a.chmlUser = profile.Chml
	a.authMu.Unlock()
	a.emitAuth(qzhuaAuthEvent{
		Status:   "logged_in",
		Username: qzhuaUserName(user),
		Email:    user.Email,
		Picture:  qzhuaUserPicture(user),
		APIError: apiError,
		Profile:  profile,
	})
}

func fetchQZhuaUser(ctx context.Context, client *http.Client, endpoint, accessToken string) (qzhuaUser, error) {
	var user qzhuaUser
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
	if user.Username == "" {
		switch {
		case user.PreferredUsername != "":
			user.Username = user.PreferredUsername
		case user.Nickname != "":
			user.Username = user.Nickname
		case user.Name != "":
			user.Username = user.Name
		}
	}
	return user, nil
}

func qzhuaUserName(user qzhuaUser) string {
	for _, value := range []string{user.Username, user.PreferredUsername, user.Nickname, user.Name, user.Email} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "轻爪用户"
}

func qzhuaUserPicture(user qzhuaUser) string {
	if user.Picture != "" {
		return user.Picture
	}
	return user.Avatar
}

func (a *App) LogoutQZhua() {
	a.stopQZhuaAttempt()
	a.authMu.Lock()
	a.qzhuaUser = qzhuaUser{}
	a.qzhuaToken = ""
	a.chmlUser = nil
	a.authMu.Unlock()
	a.emitAuth(qzhuaAuthEvent{Status: "logged_out"})
}

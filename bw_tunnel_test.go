package main

import "testing"

// TestIsFatalTunnelErr 守住"哪些建隧道失败值得重试"这条判定线。
// 早期实现用裸 "token"/"vip" 做子串匹配，会把可重试错误直接判死，
// 白白丢掉剩下 3 次重试机会，所以这里必须同时覆盖应 fatal 和不应 fatal 两侧。
func TestIsFatalTunnelErr(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		// ---- 应当 fatal（重试也没用）----
		{"隧道数量已达上限", true},
		{"创建隧道失败: 超出配额", true},
		{"仅限VIP节点使用", true},
		{"该功能为会员专享", true},
		{"权限不足", true},
		{"未登录，请先登录", true},
		{"登录已过期，请重新登录", true},
		{"HTTP 401 Unauthorized", true},
		{"403 forbidden", true},
		{"invalid token", true},
		{"token已过期", true},
		{"该隧道不属于你", true},

		// ---- 不应 fatal（可重试，必须放行）----
		{"nodetoken 校验失败", false},        // 回归：裸 "token" 会误判
		{"node_token 校验失败，请重试", false}, // 同上，node_token 与登录 token 是两回事
		{"节点 vip 线路繁忙，请稍后重试", false}, // 回归：裸 "vip" 会误判
		{"隧道名不符合规范", false},                 // 名字每次重试都会重新生成
		{"address already in use", false},
		{"connection reset by peer", false},
		{"请求超时", false},
		{"", false},
	}

	for _, c := range cases {
		if got := isFatalTunnelErr(c.msg); got != c.want {
			t.Errorf("isFatalTunnelErr(%q) = %v，期望 %v", c.msg, got, c.want)
		}
	}
}

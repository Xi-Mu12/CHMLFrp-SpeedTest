package main

import "testing"

// TestParsePortRange 覆盖 /nodeinfo 的 rport 各种写法。
// 该字段决定建隧道的 remote_port 合法区间，解析错了就会被服务端拒绝。
func TestParsePortRange(t *testing.T) {
	cases := []struct {
		raw    string
		lo, hi int
		wantOK bool
	}{
		{"8002-9000", 8002, 9000, true},
		{"8002 - 9000", 8002, 9000, true},
		{"8002~9000", 8002, 9000, true},
		{"8002至9000", 8002, 9000, true},
		{"8002", 8002, 8002, true},
		{"  8002-9000  ", 8002, 9000, true},
		{"", 0, 0, false},
		{"abc", 0, 0, false},
		{"9000-8002", 0, 0, false},  // 上下界颠倒
		{"0-9000", 0, 0, false},     // 端口 0 非法
		{"8002-70000", 0, 0, false}, // 超出 65535
		{"8002-abc", 0, 0, false},
	}
	for _, c := range cases {
		lo, hi, ok := parsePortRange(c.raw)
		if ok != c.wantOK || lo != c.lo || hi != c.hi {
			t.Errorf("parsePortRange(%q) = (%d,%d,%v)，期望 (%d,%d,%v)", c.raw, lo, hi, ok, c.lo, c.hi, c.wantOK)
		}
	}
}

// TestFilterNodesByGroup 覆盖分组过滤的全部入口值：
// 空串 / all / 全部 都表示不过滤，其余按 nodegroup 精确（不区分大小写）匹配，多个值用逗号分隔。
func TestFilterNodesByGroup(t *testing.T) {
	nodes := []NodeBasic{
		{ID: 1, Name: "广州多线", NodeGroup: "user"},
		{ID: 2, Name: "香港-高级", NodeGroup: "vip"},
		{ID: 3, Name: "上海多线", NodeGroup: "user"},
	}

	cases := []struct {
		name    string
		group   string
		wantIDs []int
	}{
		{"空串不过滤", "", []int{1, 2, 3}},
		{"all 不过滤", "all", []int{1, 2, 3}},
		{"中文全部不过滤", "全部", []int{1, 2, 3}},
		{"只取免费", "user", []int{1, 3}},
		{"只取会员", "vip", []int{2}},
		{"大小写不敏感", "VIP", []int{2}},
		{"逗号分隔多组", "user,vip", []int{1, 2, 3}},
		{"带空格的逗号分隔", " user , vip ", []int{1, 2, 3}},
		{"未知分组返回空", "unknown", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FilterNodesByGroup(nodes, c.group)
			if len(got) != len(c.wantIDs) {
				t.Fatalf("数量不符：got %d want %d（%+v）", len(got), len(c.wantIDs), got)
			}
			for i, id := range c.wantIDs {
				if got[i].ID != id {
					t.Errorf("第 %d 个节点应为 #%d，实际 #%d", i+1, id, got[i].ID)
				}
			}
		})
	}
}

// TestFetchNodeIPsShortCircuit 无 token 或空节点列表时必须直接返回空 map，
// 不能对每个节点发一次注定 401 的 /nodeinfo 请求。
func TestFetchNodeIPsShortCircuit(t *testing.T) {
	nodes := []NodeBasic{{ID: 1, Name: "广州多线"}}

	if got := FetchNodeIPs(nodes, ""); len(got) != 0 {
		t.Fatalf("空 token 应返回空 map，实际 %+v", got)
	}
	if got := FetchNodeIPs(nodes, "   "); len(got) != 0 {
		t.Fatalf("空白 token 应返回空 map，实际 %+v", got)
	}
	if got := FetchNodeIPs(nil, "some-token"); len(got) != 0 {
		t.Fatalf("空节点列表应返回空 map，实际 %+v", got)
	}
	if got := FetchNodeIPs(nodes, ""); got == nil {
		t.Fatal("应返回空 map 而不是 nil")
	}
}

package handler

import (
	"strings"
	"testing"
)

// TestValidateScopeJSON J4 fail-closed：scope_json 结构校验（与 security.ScopeFromProjectJSONString 同口径）。
func TestValidateScopeJSON(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		errSub  string
	}{
		{"空串放行", "", false, ""},
		{"空白放行", "   ", false, ""},
		{"合法 targets/exclude", `{"targets":["192.168.1.0/24","example.com"],"exclude":["10.1.1.1"],"notes":"x"}`, false, ""},
		{"合法空对象", `{}`, false, ""},
		{"非法 JSON", `{not json`, true, "不是合法 JSON"},
		{"JSON 数组整体", `["a","b"]`, true, "不是合法 JSON"},
		{"纯文本", `hello`, true, "不是合法 JSON"},
		{"targets 非数组", `{"targets":"10.0.0.1"}`, true, "targets 必须是字符串数组"},
		{"exclude 非数组", `{"exclude":{"bad":1}}`, true, "exclude 必须是字符串数组"},
		{"targets 数组含非字符串", `{"targets":[1,2]}`, true, "targets 必须是字符串数组"},
		{"notes 非数组字段不校验", `{"notes":"任意文本"}`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateScopeJSON(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("期望报错，got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望放行，got %v", err)
			}
			if tc.wantErr && tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("错误信息应含 %q，got %q", tc.errSub, err.Error())
			}
		})
	}
}

package main

import "testing"

// E4：锁住 D1-R1 的分派逻辑——带子命令走 CLI，无参数或只带 GUI 参数走 GUI。
func TestIsGUIInvocation(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{"--serve"}, true},
		{[]string{"--serve", "--no-open", "--port", "18080"}, true},
		{[]string{"--no-open"}, true},
		{[]string{"--port", "18080"}, true},

		{[]string{"scan"}, false},
		{[]string{"scan", "--raw"}, false},
		{[]string{"restore"}, false},
		{[]string{"restore", "--apply"}, false},
		{[]string{"version"}, false},
		{[]string{"--version"}, false},
		{[]string{"help"}, false},
		{[]string{"--help"}, false},
		{[]string{"-h"}, false},

		// 未知词交给 CLI 分支报「未知命令」，不能被当成 GUI 参数吞掉
		{[]string{"bogus"}, false},
	}
	for _, c := range cases {
		if got := isGUIInvocation(c.args); got != c.want {
			t.Errorf("isGUIInvocation(%q) = %v，期望 %v", c.args, got, c.want)
		}
	}
}

func TestParseGUIFlags(t *testing.T) {
	opts, err := parseGUIFlags([]string{"--serve", "--no-open", "--port", "18080"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !opts.serve || !opts.noOpen || opts.port != 18080 {
		t.Errorf("解析结果不对: %+v", opts)
	}

	// 默认值：随机端口、开浏览器
	def, err := parseGUIFlags(nil)
	if err != nil {
		t.Fatalf("空参数解析失败: %v", err)
	}
	if def.port != 0 || def.noOpen || def.serve {
		t.Errorf("默认值不对: %+v", def)
	}

	// 多余的位置参数应报错，避免把 CLI 参数误当 GUI 参数
	if _, err := parseGUIFlags([]string{"--serve", "extra"}); err == nil {
		t.Error("多余位置参数应报错")
	}
}

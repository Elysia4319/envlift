package vcompare

import "testing"

func TestCompareNumeric(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// 字符串比较会错的两个经典例子
		{"9", "17", -1},
		{"1.9", "1.10", -1},
		{"17", "9", 1},

		{"1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.4", -1},
		{"v22.22.2", "22.22.2", 0},
		{"1.143.1", "1.0.143.1", 1},
		{"17.0.20.101", "21.0.12.101", -1},
		{"20260927", "20250101", 1},
		{"7.6.9.0", "7.6.9", 0},
		{"5.1.26100.9711", "5.1.26100.9711", 0},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if !ok {
			t.Errorf("Compare(%q,%q) 应可解析", c.a, c.b)
			continue
		}
		if got != c.want {
			t.Errorf("Compare(%q,%q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPrereleaseIsLower(t *testing.T) {
	if got, ok := Compare("1.2.3-rc1", "1.2.3"); !ok || got != -1 {
		t.Errorf("预发布应低于正式版: %d ok=%v", got, ok)
	}
	if got, ok := Compare("1.2.3", "1.2.3-beta"); !ok || got != 1 {
		t.Errorf("正式版应高于预发布: %d ok=%v", got, ok)
	}
	if got, ok := Compare("1.2.3+build7", "1.2.3"); !ok || got != 0 {
		t.Errorf("build 元数据应被忽略: %d ok=%v", got, ok)
	}
}

func TestUnparseable(t *testing.T) {
	for _, s := range []string{"", "abc", "1.x.3", "v", "1..3", "-rc1"} {
		if _, ok := Compare(s, "1.0"); ok {
			t.Errorf("%q 不应被解析为版本号", s)
		}
	}
	if Valid("1.2.3") != true {
		t.Error("1.2.3 应有效")
	}
}

package sysinfo

import "testing"

// B2：build → 主版本判定表。区间外必须 unknown，不猜。
func TestMajorVersionFromBuild(t *testing.T) {
	cases := []struct {
		build int
		want  string
	}{
		{10240, "Windows 10"}, // 1507，Win10 首个正式版
		{19045, "Windows 10"}, // 22H2，Win10 最后一个正式版
		{22000, "Windows 11"}, // 21H2，Win11 首个正式版
		{26100, "Windows 11"}, // 24H2
		{26300, "Windows 11"}, // 本机实测 build
		{9999, "unknown"},     // 低于 Win10 区间
		{20000, "unknown"},    // 落在两区间之间的空隙
		{0, "unknown"},
	}
	for _, c := range cases {
		if got := majorVersionFromBuild(c.build); got != c.want {
			t.Errorf("majorVersionFromBuild(%d) = %q, want %q", c.build, got, c.want)
		}
	}
}

func TestOSLabel(t *testing.T) {
	info := Info{
		OSVersion:        "Windows 11",
		OSBuild:          "26300",
		OSDisplayVersion: "26H2",
	}
	if got, want := info.OSLabel(), "Windows 11 26H2 (build 26300)"; got != want {
		t.Errorf("OSLabel() = %q, want %q", got, want)
	}

	// 缺 display_version 时不应留下多余空格。
	info.OSDisplayVersion = ""
	if got, want := info.OSLabel(), "Windows 11 (build 26300)"; got != want {
		t.Errorf("OSLabel() = %q, want %q", got, want)
	}
}

func TestWingetHelpHasForce(t *testing.T) {
	if !wingetHelpHasForce("  --force   Direct run the command") {
		t.Error("帮助文本含 --force 时应判定支持")
	}
	if wingetHelpHasForce("  --location   Location to install to") {
		t.Error("帮助文本不含 --force 时不应误判")
	}
}

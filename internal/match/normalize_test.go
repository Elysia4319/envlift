package match

import "testing"

func TestNormalizeRealWorldNames(t *testing.T) {
	cases := []struct{ in, want string }{
		// M3 指定的三个样例
		{"Git version 2.47.1", "git"},
		{"Microsoft Visual Studio Code (User)", "microsoft visual studio code"},
		{"Adobe Illustrator 2026", "adobe illustrator"},

		// 真实注册表里常见的脏名字
		{"Python 3.13.12 (64-bit)", "python"},
		{"7-Zip 24.09 (x64)", "7-zip"},
		{"Node.js", "node.js"},
		{"Bandizip", "bandizip"},
		{"Microsoft Visual C++ 2015-2022 Redistributable (x64)", "microsoft visual c++ 2015-2022 redistributable"},
		{"Eclipse Temurin JDK with Hotspot 17.0.20.101 (x64)", "eclipse temurin jdk with hotspot"},
		{"Notepad++ (64-bit x64)", "notepad++"},
		// 单数字的主版本也会被剥掉：PowerShell 5 与 7 归一后都落到同一条配方，
		// 靠 winget_ids 区分具体版本。
		{"PowerShell 7-x64", "powershell"},
		{"VLC media player", "vlc media player"},
		{"  Extra   Spaces  ", "extra spaces"},
		{"Docker Desktop 4.35.1", "docker desktop"},
		{"Tool® Name™", "tool name"},
		{"Foo 2024 v1.2", "foo"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	for _, in := range []string{
		"Git version 2.47.1",
		"Microsoft Visual Studio Code (User)",
		"Adobe Illustrator 2026",
		"PowerShell 7-x64",
	} {
		once := Normalize(in)
		if twice := Normalize(once); twice != once {
			t.Errorf("不幂等: %q → %q → %q", in, once, twice)
		}
	}
}

func TestNormalizeEmpty(t *testing.T) {
	if got := Normalize(""); got != "" {
		t.Errorf("空串应返回空串，实际 %q", got)
	}
}

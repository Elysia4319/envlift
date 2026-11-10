package probe

import (
	"context"
	"testing"
	"time"
)

// 这些是纯函数测试，不执行任何命令、不依赖本机环境。
func TestParseVersion(t *testing.T) {
	cases := []struct{ out, want string }{
		{"Python 3.13.12\n", "3.13.12"},
		{"v22.22.2\n", "22.22.2"},
		{`openjdk version "17.0.20.1" 2025-01-21`, "17.0.20.1"},
		{"go version go1.27.3 windows/amd64\n", "1.27.3"},
		{"git version 2.55.0.windows.1\n", "2.55.0"},
		{"cargo 1.83.0 (5ffbef321 2024-10-29)\n", "1.83.0"},
		{"10.0.402\n", "10.0.402"},
		{"5.1.26100.9711\n", "5.1.26100.9711"},
		{"", ""},
		{"command not found", ""},
	}
	for _, c := range cases {
		if got := parseVersion(c.out); got != c.want {
			t.Errorf("parseVersion(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestManagerOf(t *testing.T) {
	cases := []struct{ exe, want string }{
		{`C:\Users\u\AppData\Roaming\fnm\node-versions\v22.22.2\installation\node.exe`, "fnm"},
		{`C:\Users\u\AppData\Roaming\nvm\v20.0.0\node.exe`, "nvm"},
		{`C:\Users\u\.pyenv\pyenv-win\shims\python.bat`, "pyenv"},
		{`D:\Conda\miniconda3\python.exe`, "conda"},
		{`C:\Users\u\anaconda3\python.exe`, "conda"},
		{`C:\Users\u\.sdkman\candidates\java\current\bin\java.exe`, "sdkman"},
		{`C:\Users\u\.rustup\toolchains\stable-x86_64-pc-windows-msvc\bin\cargo.exe`, "rustup"},
		{`C:\Program Files\Git\cmd\git.exe`, ""},
		{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, ""},
	}
	for _, c := range cases {
		got := managerOf(c.exe)
		if c.want == "" {
			if got != nil {
				t.Errorf("managerOf(%q) = %q, want nil", c.exe, *got)
			}
			continue
		}
		if got == nil {
			t.Errorf("managerOf(%q) = nil, want %q", c.exe, c.want)
			continue
		}
		if *got != c.want {
			t.Errorf("managerOf(%q) = %q, want %q", c.exe, *got, c.want)
		}
	}
}

type fakeResolver struct{ m map[string]Target }

func (f fakeResolver) Resolve(name string) (Target, bool) {
	t, ok := f.m[name]
	return t, ok
}

// TestProbeExecutesResolvedPath 是 A2 的回归测试：
// 执行时使用的必须是解析出来的那个绝对路径，而不是让 LookPath 重新解析。
func TestProbeExecutesResolvedPath(t *testing.T) {
	var executedPath string
	p := execProber{
		timeout: time.Second,
		run: func(ctx context.Context, path string, args []string) ([]byte, error) {
			executedPath = path
			return []byte("Python 3.99.1"), nil
		},
	}
	resolver := fakeResolver{m: map[string]Target{
		"python": {Path: `D:/Conda/miniconda3/python.exe`, Source: "user", Index: 0},
	}}

	got := p.Probe([]Spec{{Name: "python", Args: []string{"--version"}}}, resolver)
	if len(got) != 1 {
		t.Fatalf("结果数 = %d", len(got))
	}
	if executedPath != `D:/Conda/miniconda3/python.exe` {
		t.Errorf("执行路径 = %q，期望解析出的绝对路径", executedPath)
	}
	if got[0].Executable == nil || *got[0].Executable != executedPath {
		t.Error("记录的可执行文件必须与实际执行的路径一致")
	}
	if got[0].Version == nil || *got[0].Version != "3.99.1" {
		t.Errorf("版本解析错误: %#v", got[0].Version)
	}
	if got[0].Source != "user" || got[0].Index != 0 {
		t.Errorf("来源信息错误: %s/%d", got[0].Source, got[0].Index)
	}
}

func TestProbeUnknownWhenNotResolved(t *testing.T) {
	p := execProber{timeout: time.Second, run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("未解析到命令时不应执行任何进程")
		return nil, nil
	}}
	got := p.Probe([]Spec{{Name: "go"}}, fakeResolver{})
	if got[0].Version != nil || got[0].Executable != nil {
		t.Errorf("未安装应全部为 nil: %#v", got[0])
	}
}

func TestProbeTimeoutIsUnknown(t *testing.T) {
	p := execProber{
		timeout: 10 * time.Millisecond,
		run: func(ctx context.Context, path string, args []string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	resolver := fakeResolver{m: map[string]Target{"java": {Path: `C:/jdk/bin/java.exe`, Source: "machine", Index: 0}}}
	got := p.Probe([]Spec{{Name: "java", Args: []string{"-version"}}}, resolver)
	if got[0].Version != nil {
		t.Error("超时应标 unknown")
	}
	if got[0].Executable == nil {
		t.Error("超时仍应保留已解析到的路径")
	}
}

package config

import (
	"reflect"
	"strings"
	"testing"
)

// 测试固定使用假用户名与假主机名。
// 规则：任何测试都不得依赖本机的真实用户名、主机名、盘符或已装软件，
// 否则换台机器就红，而在开发机上又因为断言值恰好等于真实值而「通过得毫无意义」。
const (
	fakeUser = "olduser"
	fakeHost = "OLD-PC"
)

// sampleConfig 构造一份覆盖所有字段的「已脱敏」配置，供脱敏与读写测试复用。
func sampleConfig() *Config {
	return &Config{
		App:         AppName,
		Schema:      CurrentSchema,
		GeneratedAt: "2026-10-27T10:05:00+08:00",
		SourceMachine: SourceMachine{
			OS: OSInfo{
				Version:        "Windows 11",
				Build:          "26100",
				ProductName:    "Windows 11 Pro",
				DisplayVersion: "24H2",
			},
			Arch:     "amd64",
			Hostname: HostPlaceholder,
			Username: UserPlaceholder,
		},
		DriveMap: map[string]DriveInfo{
			"C": {Label: "System", Purpose: ""},
			"D": {Label: "Data", Purpose: ""},
		},
		Path: PathSection{
			User: []PathEntry{
				{Value: `C:\Users\${USER}\AppData\Local\Microsoft\WindowsApps`},
				{Value: `%USERPROFILE%\bin`, Expand: true},
			},
			Machine: []PathEntry{
				{Value: `C:\Program Files\Git\cmd`},
			},
		},
		Runtimes: []Runtime{
			{
				Name:       "python",
				Command:    "python",
				Version:    StringPtr("3.13.12"),
				Manager:    StringPtr("conda"),
				Executable: StringPtr(`C:\Users\${USER}\miniconda3\python.exe`),
			},
			{
				Name:    "java",
				Command: "java",
				Version: nil,
			},
		},
		Software: []Software{
			{
				Name:            "Git",
				Version:         StringPtr("2.47.1"),
				InstallLocation: StringPtr(`C:\Program Files\Git`),
				WingetID:        StringPtr("Git.Git"),
				Sources:         []string{"registry", "winget"},
				SilentCapable:   true,
				ManualHint:      nil,
			},
		},
		Installers: []Installer{
			{
				Name:         "git",
				Match:        "Git-*-64-bit.exe",
				SilentArgs:   "/VERYSILENT /NORESTART",
				NeedsAdmin:   true,
				Order:        10,
				Verify:       &Verify{Command: "git --version", ExpectContains: "git version"},
				ExpectReboot: false,
				Source:       InstallerBuiltin,
				VerifiedAt:   StringPtr("2026-01-15"),
			},
		},
	}
}

// TestRedactForwardProducesPlaceholders 是「正向脱敏」的独立测试：
// 输入真实形态的路径，断言输出必须是占位符，且不残留任何真实值。
func TestRedactForwardProducesPlaceholders(t *testing.T) {
	r := NewRedactor(fakeUser, fakeHost)

	cases := []struct{ in, want string }{
		{`C:\Users\olduser\AppData\Local\Programs`, `C:\Users\${USER}\AppData\Local\Programs`},
		{`C:\Users\olduser`, `C:\Users\${USER}`},
		{`c:\users\OLDUSER\bin`, `c:\Users\${USER}\bin`},
		{`D:\tools\olduser\bin`, `D:\tools\${USER}\bin`},
		{`\\OLD-PC\share\tools`, `\\${HOST}\share\tools`},
		{`https://OLD-PC:8080/x`, `https://${HOST}:8080/x`},
	}
	for _, c := range cases {
		got := r.redactString(c.in)
		if got != c.want {
			t.Errorf("redact(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
		if strings.Contains(strings.ToLower(got), strings.ToLower(fakeUser)) ||
			strings.Contains(strings.ToLower(got), strings.ToLower(fakeHost)) {
			t.Errorf("redact(%q) 仍残留真实值: %q", c.in, got)
		}
	}
}

func TestRedactIdempotent(t *testing.T) {
	r := NewRedactor(fakeUser, fakeHost)
	in := `C:\Users\olduser\bin;\\OLD-PC\share`
	once := r.redactString(in)
	if twice := r.redactString(once); twice != once {
		t.Errorf("脱敏不幂等:\n once  %q\n twice %q", once, twice)
	}
}

func TestRedactLeavesNativeVarsAlone(t *testing.T) {
	r := NewRedactor(fakeUser, fakeHost)
	in := `%USERPROFILE%\bin`
	if got := r.redactString(in); got != in {
		t.Errorf("不应改写 Windows 原生变量: got %q", got)
	}
}

func TestApplyThenRestoreRoundTrip(t *testing.T) {
	r := NewRedactor(fakeUser, fakeHost)

	cfg := sampleConfig()
	if err := r.Restore(cfg); err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}

	if got, want := cfg.Path.User[0].Value, `C:\Users\olduser\AppData\Local\Microsoft\WindowsApps`; got != want {
		t.Errorf("PATH 展开错误\n got  %q\n want %q", got, want)
	}
	if got, want := *cfg.Runtimes[0].Executable, `C:\Users\olduser\miniconda3\python.exe`; got != want {
		t.Errorf("Executable 展开错误\n got  %q\n want %q", got, want)
	}
	if cfg.SourceMachine.Username != fakeUser || cfg.SourceMachine.Hostname != fakeHost {
		t.Errorf("机器信息展开错误: %#v", cfg.SourceMachine)
	}

	r.Apply(cfg)
	if !reflect.DeepEqual(cfg, sampleConfig()) {
		t.Errorf("脱敏结果与初始配置不一致\n got  %#v\n want %#v", cfg, sampleConfig())
	}
}

func TestRawDisablesRedaction(t *testing.T) {
	r := NewRedactor(fakeUser, fakeHost)
	cfg := sampleConfig()
	if err := r.Restore(cfg); err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}
	snapshot := *cfg

	raw := NewRedactor(fakeUser, fakeHost)
	raw.Raw = true
	raw.Apply(cfg)

	if !reflect.DeepEqual(*cfg, snapshot) {
		t.Error("--raw 模式下不应发生任何改写")
	}
}

func TestRestoreFailsWhenUsernameUnknown(t *testing.T) {
	cfg := sampleConfig() // 含 ${USER}
	r := NewRedactor("", fakeHost)
	if err := r.Restore(cfg); err == nil {
		t.Error("含 ${USER} 但用户名未知时应报错")
	}
}

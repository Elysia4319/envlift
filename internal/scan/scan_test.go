package scan

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/probe"
	"github.com/Elysia4319/envlift/internal/sysinfo"
	"github.com/Elysia4319/envlift/internal/winreg"
)

// 测试固定使用假身份，不依赖本机真实用户名、主机名、盘符或已装软件。
const (
	fakeUser = "olduser"
	fakeHost = "OLD-PC"
)

type fakeRegistry struct {
	user, machine       winreg.Value
	userErr, machineErr error
}

func (f fakeRegistry) ReadString(hive winreg.Hive, keyPath, name string) (winreg.Value, error) {
	if hive == winreg.HiveCurrentUser {
		return f.user, f.userErr
	}
	return f.machine, f.machineErr
}

type fakeInfo struct{ info sysinfo.Info }

func (f fakeInfo) Collect() (sysinfo.Info, error) { return f.info, nil }

type fakeProber struct{ results []probe.Result }

func (f fakeProber) Probe(specs []probe.Spec, r probe.Resolver) []probe.Result { return f.results }

func notesAt(notes []Note, idx int) []string {
	for _, n := range notes {
		if n.Index == idx {
			return n.Flags
		}
	}
	return nil
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func TestParsePathKeepsRawValuesAndMarksProblems(t *testing.T) {
	raw := `C:\Windows;%SystemRoot%\bin;;C:\Windows\;` + `"C:\Quoted"` + `;C:\Missing`
	v := winreg.Value{String: raw, Expand: true}
	exists := func(p string) bool { return p != `C:\Missing` }

	entries, notes := parsePath(v, exists)

	if len(entries) != 6 {
		t.Fatalf("条目数 = %d，期望 6: %#v", len(entries), entries)
	}
	// 原始字面值必须保留，不展开、不裁剪、不删除。
	if entries[0].Value != `C:\Windows` {
		t.Errorf("第 1 条被改写: %q", entries[0].Value)
	}
	if entries[1].Value != `%SystemRoot%\bin` {
		t.Errorf("REG_EXPAND_SZ 值被展开或改写: %q", entries[1].Value)
	}
	if entries[2].Value != "" {
		t.Errorf("空条目不应被删除: %q", entries[2].Value)
	}
	if entries[3].Value != `C:\Windows\` {
		t.Errorf("尾部反斜杠不应被裁剪: %q", entries[3].Value)
	}
	if entries[5].Value != `C:\Missing` {
		t.Errorf("失效条目不应被删除: %q", entries[5].Value)
	}
	for i, e := range entries {
		if !e.Expand {
			t.Errorf("第 %d 条未继承 REG_EXPAND_SZ 标记", i+1)
		}
	}

	if !hasFlag(notesAt(notes, 2), FlagEmpty) {
		t.Error("空条目应标 empty")
	}
	if !hasFlag(notesAt(notes, 3), FlagTrailingBackslash) {
		t.Error("尾部反斜杠应标 trailing-backslash")
	}
	if !hasFlag(notesAt(notes, 3), FlagDuplicate) {
		t.Error("C:\\Windows\\ 与 C:\\Windows 应判为重复")
	}
	if !hasFlag(notesAt(notes, 4), FlagQuoted) {
		t.Error("带引号条目应标 quoted")
	}
	if !hasFlag(notesAt(notes, 5), FlagInvalid) {
		t.Error("不存在的路径应标 invalid")
	}
	if len(notesAt(notes, 0)) != 0 {
		t.Errorf("正常条目不应有标注: %v", notesAt(notes, 0))
	}
}

func TestParsePathDoesNotExpandExpandSz(t *testing.T) {
	v := winreg.Value{String: `%SystemRoot%\system32`, Expand: true}
	entries, _ := parsePath(v, func(string) bool { return true })
	if entries[0].Value != `%SystemRoot%\system32` {
		t.Errorf("不得展开 %%VAR%%: %q", entries[0].Value)
	}
}

func newTestScanner(reg winreg.Reader, info sysinfo.Info, results []probe.Result) *Scanner {
	return &Scanner{
		Registry:    reg,
		SysInfo:     fakeInfo{info: info},
		Prober:      fakeProber{results: results},
		Redactor:    config.NewRedactor("", ""),
		Exists:      func(string) bool { return true },
		Expand:      func(p string) string { return p },
		Lookup:      func(p string) (string, bool) { return p, true },
		SessionPath: func() string { return "" },
		Now:         func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		Specs:       []probe.Spec{{Name: "python"}},
	}
}

func sampleInfo() sysinfo.Info {
	return sysinfo.Info{
		OSVersion:        "Windows 11",
		OSBuild:          "26100",
		OSProductName:    "Windows 11 Pro",
		OSDisplayVersion: "24H2",
		Arch:             "amd64",
		Username:         fakeUser,
		Hostname:         fakeHost,
		Drives:           []sysinfo.Drive{{Letter: "C:", Label: "System"}},
		WingetAvailable:  true,
		WingetVersion:    "1.11.0",
	}
}

func TestScannerRunProducesRedactedConfig(t *testing.T) {
	reg := fakeRegistry{
		user:    winreg.Value{String: `C:\Users\olduser\bin;%USERPROFILE%\go\bin`, Expand: true},
		machine: winreg.Value{String: `C:\Windows\system32`},
	}
	results := []probe.Result{
		{
			Name: "python", Command: "python",
			Version:    config.StringPtr("3.13.12"),
			Manager:    config.StringPtr("conda"),
			Executable: config.StringPtr(`C:\Users\olduser\miniconda3\python.exe`),
		},
		{Name: "java", Command: "java"},
	}

	res, err := newTestScanner(reg, sampleInfo(), results).Run()
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if got, want := res.Config.Path.User[0].Value, `C:\Users\${USER}\bin`; got != want {
		t.Errorf("PATH 未脱敏\n got  %q\n want %q", got, want)
	}
	if !res.Config.Path.User[0].Expand {
		t.Error("REG_EXPAND_SZ 标记丢失")
	}
	if got := res.Config.SourceMachine.Username; got != config.UserPlaceholder {
		t.Errorf("username 未脱敏: %q", got)
	}
	if got := res.Config.SourceMachine.Hostname; got != config.HostPlaceholder {
		t.Errorf("hostname 未脱敏: %q", got)
	}
	if got, want := *res.Config.Runtimes[0].Executable, `C:\Users\${USER}\miniconda3\python.exe`; got != want {
		t.Errorf("运行时路径未脱敏\n got  %q\n want %q", got, want)
	}
	if res.Config.Runtimes[1].Version != nil {
		t.Error("未探测到版本时应为 nil")
	}
	if res.Config.DriveMap["C"].Label != "System" {
		t.Errorf("drive_map 未取自卷标: %#v", res.Config.DriveMap)
	}
	if res.Config.DriveMap["C"].Purpose != "" {
		t.Error("purpose 必须留空，scan 不应猜")
	}
	if got, want := res.Config.GeneratedAt, "2026-01-01T00:00:00Z"; got != want {
		t.Errorf("generated_at = %q, want %q", got, want)
	}
	if res.Config.Schema != config.CurrentSchema {
		t.Errorf("schema = %d", res.Config.Schema)
	}
}

func TestScannerRawModeKeepsRealValues(t *testing.T) {
	reg := fakeRegistry{user: winreg.Value{String: `C:\Users\olduser\bin`}}
	s := newTestScanner(reg, sampleInfo(), nil)
	s.Redactor.Raw = true

	res, err := s.Run()
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if got, want := res.Config.Path.User[0].Value, `C:\Users\olduser\bin`; got != want {
		t.Errorf("--raw 不应脱敏\n got  %q\n want %q", got, want)
	}
	if res.Config.SourceMachine.Username != fakeUser {
		t.Errorf("--raw 下 username 应为真实值: %q", res.Config.SourceMachine.Username)
	}
}

func TestScannerWarnsOnRegistryErrorInsteadOfFailing(t *testing.T) {
	reg := fakeRegistry{
		userErr: errors.New("拒绝访问"),
		machine: winreg.Value{String: `C:\Windows\system32`},
	}
	res, err := newTestScanner(reg, sampleInfo(), nil).Run()
	if err != nil {
		t.Fatalf("读注册表失败不应让整个扫描失败: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("应产生警告")
	}
	if !strings.Contains(res.Warnings[0], "拒绝访问") {
		t.Errorf("警告应带上具体原因: %v", res.Warnings)
	}
	if len(res.Config.Path.Machine) != 1 {
		t.Error("系统级 PATH 仍应正常采集")
	}
}

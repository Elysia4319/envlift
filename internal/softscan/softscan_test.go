package softscan

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/winreg"
)

// 全部测试用假 Tree，不读本机注册表（E2）。

type fakeTree struct {
	subs   map[string][]string
	values map[string]winreg.Values
}

func tkey(hive winreg.Hive, path string, view winreg.View) string {
	return fmt.Sprintf("%d|%s|%d", hive, path, view)
}

func (f *fakeTree) SubKeys(hive winreg.Hive, keyPath string, view winreg.View) ([]string, error) {
	if s, ok := f.subs[tkey(hive, keyPath, view)]; ok {
		return s, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeTree) ReadValues(hive winreg.Hive, keyPath string, view winreg.View, strNames, dwordNames []string) (winreg.Values, error) {
	v, ok := f.values[tkey(hive, keyPath, view)]
	if !ok {
		return winreg.Values{}, errors.New("not found")
	}
	out := winreg.Values{Str: map[string]string{}, DWORD: map[string]uint32{}}
	for _, n := range strNames {
		if s, ok := v.Str[n]; ok {
			out.Str[n] = s
		}
	}
	for _, n := range dwordNames {
		if d, ok := v.DWORD[n]; ok {
			out.DWORD[n] = d
		}
	}
	return out, nil
}

func TestFilterRules(t *testing.T) {
	cases := []struct {
		name string
		e    UninstallEntry
		want string
	}{
		{"正常条目", UninstallEntry{DisplayName: "Git", UninstallString: "x"}, ""},
		{"系统组件", UninstallEntry{DisplayName: "A", SystemComponent: 1}, FilterSystemComponent},
		{"无名字无卸载串", UninstallEntry{}, FilterNoNameNoUninst},
		{"子组件", UninstallEntry{DisplayName: "A", ParentKeyName: "B"}, FilterParentKey},
		{"安全更新", UninstallEntry{DisplayName: "A", ReleaseType: "Security Update"}, FilterReleaseType},
		{"更新汇总", UninstallEntry{DisplayName: "A", ReleaseType: "Update Rollup"}, FilterReleaseType},
		{"KB 补丁", UninstallEntry{DisplayName: "KB5031234"}, FilterKBPatch},
		{"Update for", UninstallEntry{DisplayName: "Update for Office"}, FilterKBPatch},
		{"Security Update for", UninstallEntry{DisplayName: "Security Update for Edge"}, FilterKBPatch},
	}
	for _, c := range cases {
		if got := filterReason(c.e); got != c.want {
			t.Errorf("%s: 过滤原因 = %q，期望 %q", c.name, got, c.want)
		}
	}
}

func TestCollectCoversDualView(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"A"},
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View32): {"B"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\A`, winreg.View64): {
				Str: map[string]string{"DisplayName": "App 64", "DisplayVersion": "1.0"},
			},
			tkey(winreg.HiveLocalMachine, uninstallKey+`\B`, winreg.View32): {
				Str: map[string]string{"DisplayName": "App 32", "DisplayVersion": "2.0"},
			},
		},
	}

	res := Collect(ft, nil)
	if len(res.Software) != 2 {
		t.Fatalf("应采集到 2 条，实际 %d: %#v", len(res.Software), res.Software)
	}
	arch := map[string]string{}
	for _, s := range res.Software {
		if s.ArchView != nil {
			arch[s.Name] = *s.ArchView
		}
	}
	if arch["App 64"] != "64" || arch["App 32"] != "32" {
		t.Errorf("arch_view 未按视图区分: %#v", arch)
	}
}

func TestCollectCountsFiltered(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"ok", "sys", "kb"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\ok`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Good App"},
			},
			tkey(winreg.HiveLocalMachine, uninstallKey+`\sys`, winreg.View64): {
				Str:   map[string]string{"DisplayName": "Hidden"},
				DWORD: map[string]uint32{"SystemComponent": 1},
			},
			tkey(winreg.HiveLocalMachine, uninstallKey+`\kb`, winreg.View64): {
				Str: map[string]string{"DisplayName": "KB5031234"},
			},
		},
	}

	res := Collect(ft, nil)
	if len(res.Software) != 1 {
		t.Fatalf("应只剩 1 条，实际 %d", len(res.Software))
	}
	if res.Raw != 3 {
		t.Errorf("原始条目数 = %d，期望 3", res.Raw)
	}
	if res.Filtered[FilterSystemComponent] != 1 || res.Filtered[FilterKBPatch] != 1 {
		t.Errorf("过滤计数不对: %#v", res.Filtered)
	}
}

func TestCollectWarnsOnKeyError(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"broken", "ok"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\ok`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Fine"},
			},
		},
	}
	res := Collect(ft, nil)
	if len(res.Warnings) == 0 {
		t.Error("单键读取失败应产生 warning")
	}
	if len(res.Software) != 1 {
		t.Errorf("其他条目应正常采集，实际 %d", len(res.Software))
	}
}

func TestMergeAttachesWingetIDByNormalizedName(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"git"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\git`, winreg.View64): {
				Str: map[string]string{
					"DisplayName":    "Git version 2.56.0",
					"DisplayVersion": "2.56.0",
					"Publisher":      "The Git Development Community",
				},
			},
		},
	}
	runner := func(args ...string) (string, error) {
		return `[{"Name":"Git","Id":"Git.Git","Version":"2.56.0","Source":"winget"}]`, nil
	}

	res := Collect(ft, runner)
	if len(res.Software) != 1 {
		t.Fatalf("winget 包应挂到已有条目上，实际条目数 %d: %#v", len(res.Software), res.Software)
	}
	sw := res.Software[0]
	if sw.WingetID == nil || *sw.WingetID != "Git.Git" {
		t.Errorf("winget_id 未补上: %#v", sw.WingetID)
	}
	if !sw.SilentCapable {
		t.Error("有 winget id 时应标为可静默安装")
	}
	if len(sw.Sources) != 2 {
		t.Errorf("来源集合应有 uninstall + winget，实际 %#v", sw.Sources)
	}
}

func TestMergeKeepsDifferentVersionsSeparate(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"a", "b"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\a`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Tool", "DisplayVersion": "1.0", "Publisher": "P"},
			},
			tkey(winreg.HiveLocalMachine, uninstallKey+`\b`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Tool", "DisplayVersion": "2.0", "Publisher": "P"},
			},
		},
	}
	res := Collect(ft, nil)
	if len(res.Software) != 2 {
		t.Fatalf("同名不同版本应保留两条，实际 %d", len(res.Software))
	}
}

func TestMergeWingetOnlyEntry(t *testing.T) {
	ft := &fakeTree{subs: map[string][]string{}, values: map[string]winreg.Values{}}
	runner := func(args ...string) (string, error) {
		return `[{"Name":"Some Store App","Id":"Vendor.StoreApp","Version":"3.0","Source":"msstore"}]`, nil
	}
	res := Collect(ft, runner)
	if len(res.Software) != 1 {
		t.Fatalf("只有 winget 有的包应独立成条，实际 %d", len(res.Software))
	}
	if got := res.Software[0].Sources; len(got) != 1 || got[0] != "winget" {
		t.Errorf("来源应为 winget，实际 %#v", got)
	}
}

func TestAppPathsFillsInstallLocationOnly(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"tool"},
			tkey(winreg.HiveLocalMachine, appPathsKey, winreg.View64):  {"Tool.exe"},
			tkey(winreg.HiveLocalMachine, appPathsKey, winreg.View32):  {},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\tool`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Tool 1.0", "DisplayVersion": "1.0"},
			},
			tkey(winreg.HiveLocalMachine, appPathsKey+`\Tool.exe`, winreg.View64): {
				Str: map[string]string{"": `C:\Tools\Tool.exe`},
			},
		},
	}
	res := Collect(ft, nil)
	if len(res.Software) != 1 {
		t.Fatalf("App Paths 不应产生新条目，实际 %d", len(res.Software))
	}
	sw := res.Software[0]
	if sw.InstallLocation == nil || *sw.InstallLocation != `C:\Tools\Tool.exe` {
		t.Errorf("install_location 未补上: %#v", sw.InstallLocation)
	}
}

func wingetTable(rows [][5]string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-32s%-24s%-14s%-12s%s\n", "Name", "Id", "Version", "Available", "Source"))
	b.WriteString(strings.Repeat("-", 100) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-32s%-24s%-14s%-12s%s\n", r[0], r[1], r[2], r[3], r[4]))
	}
	return b.String()
}

func TestParseWingetTable(t *testing.T) {
	out := wingetTable([][5]string{
		{"Git", "Git.Git", "2.56.0", "", "winget"},
		{"Adobe Illustrator 2026", `ARP\Machine\X86\ILST`, "30.8.2", "", ""},
	})
	pkgs := ParseWingetTable(out)
	if len(pkgs) != 2 {
		t.Fatalf("解析出 %d 条，期望 2: %#v", len(pkgs), pkgs)
	}
	if pkgs[0].ID != "Git.Git" || pkgs[0].Version != "2.56.0" || pkgs[0].Source != "winget" {
		t.Errorf("首条解析错误: %#v", pkgs[0])
	}
	if pkgs[1].Name != "Adobe Illustrator 2026" || pkgs[1].Version != "30.8.2" {
		t.Errorf("次条解析错误: %#v", pkgs[1])
	}
}

func TestParseWingetCSV(t *testing.T) {
	out := "Name,Id,Version,Available,Source\nGit,Git.Git,2.56.0,,winget\n\"App, With Comma\",Vendor.App,1.0,,winget\n"
	pkgs, err := ParseWingetCSV(out)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("解析出 %d 条", len(pkgs))
	}
	if pkgs[1].Name != "App, With Comma" {
		t.Errorf("带逗号的名字解析错误: %q", pkgs[1].Name)
	}
}

func TestParseWingetJSONWrapped(t *testing.T) {
	out := `{"Sources":[{"Packages":[{"PackageIdentifier":"Git.Git","PackageName":"Git","Version":"2.56.0","Source":"winget"}]}]}`
	pkgs, err := ParseWingetJSON(out)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != "Git.Git" || pkgs[0].Name != "Git" {
		t.Errorf("解析错误: %#v", pkgs)
	}
}

func TestScanWingetFallsBackToTable(t *testing.T) {
	table := wingetTable([][5]string{{"Git", "Git.Git", "2.56.0", "", "winget"}})
	runner := func(args ...string) (string, error) {
		for _, a := range args {
			if a == "json" || a == "csv" {
				return "", errors.New("unrecognized argument")
			}
		}
		return table, nil
	}
	pkgs, via, err := ScanWinget(runner)
	if err != nil {
		t.Fatalf("应回落到表格解析: %v", err)
	}
	if via != "table" {
		t.Errorf("via = %q，期望 table", via)
	}
	if len(pkgs) != 1 || pkgs[0].ID != "Git.Git" {
		t.Errorf("解析错误: %#v", pkgs)
	}
}

func TestScanWingetErrorWhenUnavailable(t *testing.T) {
	runner := func(args ...string) (string, error) { return "", errors.New("winget 不存在") }
	if _, _, err := ScanWinget(runner); err == nil {
		t.Error("winget 不可用时应返回错误，由上层降级")
	}
}

func TestSoftwareSilentCapableOnlyWithWingetID(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"a"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\a`, winreg.View64): {
				Str: map[string]string{"DisplayName": "No Winget App", "DisplayVersion": "1.0"},
			},
		},
	}
	res := Collect(ft, nil)
	if res.Software[0].SilentCapable {
		t.Error("没有 winget id 时不应标为可静默安装")
	}
	var _ = config.Software{}
}

// winget list 会把 ARP 直挂的已装程序也列出来（Id 形如 ARP\Machine\...，Source 空）。
// 这类 Id 不是可安装的包 id，必须丢掉，否则计划会执行必然失败的 winget install。
func TestARPEntriesAreNotUsableWingetIDs(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"adobe"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\adobe`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Adobe Thing", "DisplayVersion": "1.0"},
			},
		},
	}
	runner := func(args ...string) (string, error) {
		return `[{"Name":"Adobe Thing","Id":"ARP\\Machine\\X64\\{ABC}","Version":"1.0","Source":""}]`, nil
	}
	res := Collect(ft, runner)
	if len(res.Software) != 1 {
		t.Fatalf("条目数 = %d", len(res.Software))
	}
	sw := res.Software[0]
	if sw.WingetID != nil {
		t.Errorf("ARP 直挂条目的 Id 不应被当作 winget id: %q", *sw.WingetID)
	}
	if sw.SilentCapable {
		t.Error("没有可用 winget id 时不应标为可静默安装")
	}
}

func TestRealWingetIDIsUsable(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"git"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\git`, winreg.View64): {
				Str: map[string]string{"DisplayName": "Git", "DisplayVersion": "2.56.0"},
			},
		},
	}
	runner := func(args ...string) (string, error) {
		return `[{"Name":"Git","Id":"Git.Git","Version":"2.56.0","Source":"winget"}]`, nil
	}
	res := Collect(ft, runner)
	if res.Software[0].WingetID == nil || *res.Software[0].WingetID != "Git.Git" {
		t.Errorf("真实 winget id 应被采用: %#v", res.Software[0].WingetID)
	}
}

func TestFilterNoDisplayName(t *testing.T) {
	ft := &fakeTree{
		subs: map[string][]string{
			tkey(winreg.HiveLocalMachine, uninstallKey, winreg.View64): {"nameless"},
		},
		values: map[string]winreg.Values{
			tkey(winreg.HiveLocalMachine, uninstallKey+`\nameless`, winreg.View64): {
				Str: map[string]string{"UninstallString": "x.exe"},
			},
		},
	}
	res := Collect(ft, nil)
	if len(res.Software) != 0 {
		t.Errorf("无 DisplayName 的条目应被滤掉，实际 %d 条", len(res.Software))
	}
	if res.Filtered[FilterNoDisplayName] != 1 {
		t.Errorf("应计入 no_display_name: %#v", res.Filtered)
	}
}

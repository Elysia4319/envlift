// Package softscan 只读采集已装软件：注册表 Uninstall（双视图）+ winget list + App Paths。
package softscan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/match"
	"github.com/Elysia4319/envlift/internal/winreg"
)

// uninstallKey 是逻辑路径。32/64 视图由 winreg.View 控制，不手写 Wow6432Node（C1）。
const uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

const appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths`

var (
	uninstallStrNames = []string{
		"DisplayName", "DisplayVersion", "Publisher",
		"InstallLocation", "UninstallString", "ParentKeyName", "ReleaseType",
	}
	uninstallDWORDNames = []string{"SystemComponent"}

	reKBName    = regexp.MustCompile(`(?i)^KB\d+`)
	reUpdateFor = regexp.MustCompile(`(?i)^(Update for|Security Update for|Hotfix for)\b`)
)

// 过滤原因（C2）。计数会写进扫描摘要，别静默丢弃。
const (
	FilterSystemComponent = "system_component"
	FilterNoNameNoUninst  = "no_name_no_uninstall"
	FilterParentKey       = "parent_key"
	FilterReleaseType     = "release_type"
	FilterKBPatch         = "kb_or_update_patch"
	// FilterNoDisplayName 是 C2 之外的补充：没有 DisplayName 的条目既无法展示
	// 也无法匹配，留着只会污染清单。计数同样写进摘要。
	FilterNoDisplayName = "no_display_name"
)

// UninstallEntry 是一条注册表 Uninstall 记录（过滤前）。
type UninstallEntry struct {
	KeyPath         string
	Hive            string
	ArchView        string
	DisplayName     string
	DisplayVersion  string
	Publisher       string
	InstallLocation string
	UninstallString string
	ParentKeyName   string
	ReleaseType     string
	SystemComponent uint32
}

// filterReason 返回该条目应被滤掉的原因，空串表示保留（C2）。
func filterReason(e UninstallEntry) string {
	if e.SystemComponent == 1 {
		return FilterSystemComponent
	}
	if e.DisplayName == "" && e.UninstallString == "" {
		return FilterNoNameNoUninst
	}
	if strings.TrimSpace(e.DisplayName) == "" {
		return FilterNoDisplayName
	}
	if e.ParentKeyName != "" {
		return FilterParentKey
	}
	switch strings.ToLower(strings.TrimSpace(e.ReleaseType)) {
	case "security update", "update rollup", "hotfix", "service pack":
		return FilterReleaseType
	}
	if reKBName.MatchString(e.DisplayName) || reUpdateFor.MatchString(e.DisplayName) {
		return FilterKBPatch
	}
	return ""
}

// Result 是一次软件采集的结果。
type Result struct {
	Software []config.Software
	// Filtered 按原因统计被滤掉的条目数（C2）。
	Filtered map[string]int
	// Raw 是过滤前的条目总数。
	Raw int
	// BySource 记录每个源贡献的条目数（C6）。
	BySource  map[string]int
	WingetVia string
	Warnings  []string
}

type uninstallTarget struct {
	hive  winreg.Hive
	view  winreg.View
	arch  string
	label string
}

// 三个来源（C1）。HKLM 64 与 32 用 WOW64 标志区分，HKCU 走默认视图。
var uninstallTargets = []uninstallTarget{
	{winreg.HiveLocalMachine, winreg.View64, "64", "HKLM"},
	{winreg.HiveLocalMachine, winreg.View32, "32", "HKLM"},
	{winreg.HiveCurrentUser, winreg.ViewDefault, "", "HKCU"},
}

// Collect 执行三源采集并合并。
//
// runner 用于执行 winget（测试注入假输出）；为 nil 时跳过 winget 源。
func Collect(tree winreg.Tree, runner Runner) *Result {
	res := &Result{
		Filtered: map[string]int{},
		BySource: map[string]int{},
	}

	entries := scanUninstall(tree, res)
	appPaths := scanAppPaths(tree, res)

	var wingetPkgs []WingetPackage
	if runner != nil {
		pkgs, via, err := ScanWinget(runner)
		switch {
		case err != nil:
			res.Warnings = append(res.Warnings, "winget list 不可用，该源跳过: "+err.Error())
		default:
			wingetPkgs = pkgs
			res.WingetVia = via
			res.BySource["winget"] = len(pkgs)
		}
	}

	res.Software = merge(entries, wingetPkgs, appPaths, res)
	return res
}

func scanUninstall(tree winreg.Tree, res *Result) []UninstallEntry {
	var out []UninstallEntry

	for _, t := range uninstallTargets {
		subs, err := tree.SubKeys(t.hive, uninstallKey, t.view)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("枚举 %s Uninstall（视图 %s）失败: %v",
				t.label, viewName(t.view), err))
			continue
		}
		for _, sub := range subs {
			keyPath := uninstallKey + `\` + sub
			vals, err := tree.ReadValues(t.hive, keyPath, t.view, uninstallStrNames, uninstallDWORDNames)
			if err != nil {
				// 单键读取失败（权限/损坏）记 warning 跳过，不中断整体扫描（C1）。
				res.Warnings = append(res.Warnings, fmt.Sprintf("读取 %s\\%s 失败，已跳过: %v",
					t.label, keyPath, err))
				continue
			}
			e := UninstallEntry{
				KeyPath:         keyPath,
				Hive:            t.label,
				ArchView:        t.arch,
				DisplayName:     vals.Str["DisplayName"],
				DisplayVersion:  vals.Str["DisplayVersion"],
				Publisher:       vals.Str["Publisher"],
				InstallLocation: vals.Str["InstallLocation"],
				UninstallString: vals.Str["UninstallString"],
				ParentKeyName:   vals.Str["ParentKeyName"],
				ReleaseType:     vals.Str["ReleaseType"],
				SystemComponent: vals.DWORD["SystemComponent"],
			}
			res.Raw++
			if reason := filterReason(e); reason != "" {
				res.Filtered[reason]++
				continue
			}
			out = append(out, e)
		}
	}
	res.BySource["uninstall"] = len(out)
	return out
}

func viewName(v winreg.View) string {
	switch v {
	case winreg.View64:
		return "64"
	case winreg.View32:
		return "32"
	default:
		return "default"
	}
}

// scanAppPaths 读 App Paths，用来补可执行文件路径，**不单独产生软件条目**（C4）。
func scanAppPaths(tree winreg.Tree, res *Result) map[string]string {
	out := map[string]string{}
	for _, view := range []winreg.View{winreg.View64, winreg.View32} {
		subs, err := tree.SubKeys(winreg.HiveLocalMachine, appPathsKey, view)
		if err != nil {
			continue // App Paths 缺失很常见，不记 warning
		}
		for _, sub := range subs {
			vals, err := tree.ReadValues(winreg.HiveLocalMachine, appPathsKey+`\`+sub, view, []string{""}, nil)
			if err != nil {
				continue
			}
			path := strings.TrimSpace(vals.Str[""])
			if path == "" {
				continue
			}
			base := strings.TrimSuffix(sub, ".exe")
			key := match.Normalize(base)
			if key == "" {
				continue
			}
			if _, ok := out[key]; !ok {
				out[key] = path
			}
		}
	}
	res.BySource["app_paths"] = len(out)
	return out
}

// usableWingetID 判断 winget list 里的 Id 是否真的是可安装的包 id。
//
// winget list 会把「ARP 直挂」的已装程序也列出来，其 Id 形如
// ARP\Machine\X64\{GUID}，Source 列为空——这类条目**没有对应的 winget 包**，
// 拿它去 winget install 必然失败。只有带源映射的才是可用 id。
func usableWingetID(p WingetPackage) *string {
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return nil
	}
	if strings.Contains(id, `\`) || strings.HasPrefix(strings.ToLower(id), "arp") {
		return nil
	}
	if p.Source != "" && !strings.EqualFold(p.Source, "winget") && !strings.EqualFold(p.Source, "msstore") {
		return nil
	}
	return config.StringPtr(id)
}

// merge 按 C5 合并：键为「规范化 name + publisher」，同名不同版本保留多条。
//
// winget 不提供 publisher，所以 winget 包通过「规范化 name 唯一匹配」挂到已有条目上；
// 挂不上的才作为独立条目加入（来源记 winget）。
func merge(entries []UninstallEntry, pkgs []WingetPackage, appPaths map[string]string, res *Result) []config.Software {
	type bucket struct {
		sw     config.Software
		source map[string]bool
	}
	byKey := map[string]*bucket{}
	var order []string

	addEntry := func(e UninstallEntry) {
		key := match.Normalize(e.DisplayName) + "\x00" + match.Normalize(e.Publisher) + "\x00" + strings.TrimSpace(e.DisplayVersion)
		if b, ok := byKey[key]; ok {
			b.source["uninstall"] = true
			return
		}
		sw := config.Software{
			Name:          e.DisplayName,
			Sources:       []string{"uninstall"},
			SilentCapable: false,
		}
		if e.DisplayVersion != "" {
			sw.Version = config.StringPtr(e.DisplayVersion)
		}
		if e.Publisher != "" {
			sw.Publisher = config.StringPtr(e.Publisher)
		}
		if e.InstallLocation != "" {
			sw.InstallLocation = config.StringPtr(e.InstallLocation)
		}
		if e.UninstallString != "" {
			sw.UninstallString = config.StringPtr(e.UninstallString)
		}
		if e.ArchView != "" {
			sw.ArchView = config.StringPtr(e.ArchView)
		}
		byKey[key] = &bucket{sw: sw, source: map[string]bool{"uninstall": true}}
		order = append(order, key)
	}

	for _, e := range entries {
		addEntry(e)
	}

	// 规范化 name → 已存在条目键，用于把 winget 包挂上去。
	nameIndex := map[string][]string{}
	for _, key := range order {
		n := match.Normalize(byKey[key].sw.Name)
		nameIndex[n] = append(nameIndex[n], key)
	}

	for _, p := range pkgs {
		n := match.Normalize(p.Name)
		if keys := nameIndex[n]; len(keys) == 1 {
			b := byKey[keys[0]]
			if b.sw.WingetID == nil {
				if id := usableWingetID(p); id != nil {
					b.sw.WingetID = id
					b.sw.SilentCapable = true
				}
			}
			b.source["winget"] = true
			continue
		}
		// 挂不上（同名多条或没有同名）→ 作为独立条目加入。
		key := "winget\x00" + strings.ToLower(p.ID)
		if _, ok := byKey[key]; ok {
			continue
		}
		id := usableWingetID(p)
		sw := config.Software{
			Name:          p.Name,
			Sources:       []string{"winget"},
			SilentCapable: id != nil,
		}
		if p.Version != "" {
			sw.Version = config.StringPtr(p.Version)
		}
		sw.WingetID = id
		byKey[key] = &bucket{sw: sw, source: map[string]bool{"winget": true}}
		order = append(order, key)
	}

	// App Paths 只补 install_location，不产生新条目（C4）。
	for _, key := range order {
		b := byKey[key]
		if b.sw.InstallLocation != nil {
			continue
		}
		n := match.Normalize(b.sw.Name)
		if p, ok := appPaths[n]; ok {
			b.sw.InstallLocation = config.StringPtr(p)
			b.source["app_paths"] = true
		}
	}

	out := make([]config.Software, 0, len(order))
	for _, key := range order {
		b := byKey[key]
		srcs := make([]string, 0, len(b.source))
		for _, name := range []string{"uninstall", "winget", "app_paths"} {
			if b.source[name] {
				srcs = append(srcs, name)
			}
		}
		b.sw.Sources = srcs
		out = append(out, b.sw)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

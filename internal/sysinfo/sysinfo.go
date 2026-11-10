// Package sysinfo 采集系统级只读信息：版本、架构、用户、主机名、盘符、winget、提权状态。
package sysinfo

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Elysia4319/envlift/internal/winreg"
)

const (
	currentVersionKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	maxPath           = 261
)

// Drive 是一个逻辑盘。
type Drive struct {
	Letter string // 形如 "C:"
	Label  string // 卷标，可能为空
}

// Info 是一次系统信息采集的结果。
type Info struct {
	// OSVersion 是按 build number 判定的主版本，可能是 "unknown"。
	OSVersion string
	// OSBuild 是注册表原始 build 号，保留原值以便判定出错时事后核对。
	OSBuild string
	// OSProductName 是注册表 ProductName 原值。**不可靠**，仅作参考。
	OSProductName string
	// OSDisplayVersion 是 24H2 / 26H2 这类功能更新版本号。
	OSDisplayVersion string

	Arch            string
	Username        string
	Hostname        string
	Drives          []Drive
	WingetAvailable bool
	WingetVersion   string
	Elevated        bool
	// Warnings 记录「部分失败但不影响整体」的问题，供预览展示。
	Warnings []string
}

// OSLabel 返回人类可读的系统描述，用于预览。
func (i Info) OSLabel() string {
	var b strings.Builder
	b.WriteString(i.OSVersion)
	if i.OSDisplayVersion != "" {
		b.WriteString(" ")
		b.WriteString(i.OSDisplayVersion)
	}
	if i.OSBuild != "" {
		b.WriteString(" (build ")
		b.WriteString(i.OSBuild)
		b.WriteString(")")
	}
	return strings.TrimSpace(b.String())
}

// majorVersionFromBuild 按 build number 判定主版本（B2）。
//
// 依据：Windows 10 的 build 区间为 10240（1507）到 19045（22H2）；
// Windows 11 自 22000 起（21H2）。
//
// 区间之外一律 unknown，不猜、不归入任何主版本——注册表 ProductName 在部分系统上
// 会给出错误值（本机实测 build 26300 却报 "Windows 10 Home China"），所以不能拿它
// 兜底。以后微软再改号段，只需调整这张表。
func majorVersionFromBuild(build int) string {
	switch {
	case build >= 10240 && build <= 19045:
		return "Windows 10"
	case build >= 22000:
		return "Windows 11"
	default:
		return "unknown"
	}
}

// Collector 采集系统信息，便于测试注入假实现。
type Collector interface {
	Collect() (Info, error)
}

// New 返回访问真实系统的 Collector。
func New(reg winreg.Reader) Collector { return windowsCollector{reg: reg} }

type windowsCollector struct {
	reg winreg.Reader
}

func (c windowsCollector) Collect() (Info, error) {
	var info Info
	info.Arch = runtime.GOARCH

	if v, err := c.reg.ReadString(winreg.HiveLocalMachine, currentVersionKey, "ProductName"); err == nil {
		info.OSProductName = v.String
	} else {
		info.Warnings = append(info.Warnings, "读取系统 ProductName 失败: "+err.Error())
	}
	if v, err := c.reg.ReadString(winreg.HiveLocalMachine, currentVersionKey, "DisplayVersion"); err == nil {
		info.OSDisplayVersion = v.String
	}
	if v, err := c.reg.ReadString(winreg.HiveLocalMachine, currentVersionKey, "CurrentBuildNumber"); err == nil {
		info.OSBuild = v.String
	}
	if n, err := strconv.Atoi(info.OSBuild); err == nil {
		info.OSVersion = majorVersionFromBuild(n)
	} else {
		info.OSVersion = "unknown"
		info.Warnings = append(info.Warnings, "build 号无法解析，主版本标为 unknown: "+info.OSBuild)
	}

	info.Username = os.Getenv("USERNAME")
	if info.Username == "" {
		info.Warnings = append(info.Warnings, "无法确定当前用户名（USERNAME 为空），脱敏将退化为不替换用户名")
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	} else {
		info.Warnings = append(info.Warnings, "读取主机名失败: "+err.Error())
	}

	info.Drives = listDrives()
	info.Elevated = isElevated()

	info.WingetAvailable, info.WingetVersion = wingetVersion()
	return info, nil
}

func listDrives() []Drive {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A'+i)) + ":"
		out = append(out, Drive{Letter: letter, Label: volumeLabel(letter + `\`)})
	}
	return out
}

func volumeLabel(root string) string {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	var vol [maxPath]uint16
	var fsName [maxPath]uint16
	var serial, maxComponent, flags uint32
	err = windows.GetVolumeInformation(p,
		&vol[0], uint32(len(vol)),
		&serial, &maxComponent, &flags,
		&fsName[0], uint32(len(fsName)))
	if err != nil {
		return "" // 无介质或不可读的盘符，留空而不是报错
	}
	return windows.UTF16ToString(vol[:])
}

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func wingetVersion() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "winget", "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false, ""
	}
	return true, strings.TrimSpace(string(out))
}

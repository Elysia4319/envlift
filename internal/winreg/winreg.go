// Package winreg 提供只读的注册表访问。
//
// 本包**不导出任何写操作**——扫描阶段只读是硬约束，从类型层面就不给写的能力。
package winreg

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// Hive 标识注册表根键。
type Hive int

const (
	// HiveCurrentUser 对应 HKEY_CURRENT_USER。
	HiveCurrentUser Hive = iota
	// HiveLocalMachine 对应 HKEY_LOCAL_MACHINE。
	HiveLocalMachine
)

func (h Hive) String() string {
	if h == HiveLocalMachine {
		return "HKLM"
	}
	return "HKCU"
}

func (h Hive) root() registry.Key {
	if h == HiveLocalMachine {
		return registry.LOCAL_MACHINE
	}
	return registry.CURRENT_USER
}

// View 指定注册表视图。
//
// 选择说明（C1）：这里用 **KEY_WOW64_64KEY / KEY_WOW64_32KEY 显式标志**打开同一个
// 逻辑路径，而不是直接去开 Wow6432Node 物理路径。理由：逻辑路径只写一次，不会出现
// 「一处写 SOFTWARE\...、另一处写 SOFTWARE\WOW6432Node\...」的重复与漂移；WOW64
// 重定向由系统负责，行为与资源管理器一致。
type View int

const (
	// ViewDefault 使用默认视图（跟随进程位数）。
	ViewDefault View = iota
	// View64 强制 64 位视图。
	View64
	// View32 强制 32 位视图（WOW6432Node）。
	View32
)

func (v View) access() uint32 {
	switch v {
	case View64:
		return registry.READ | registry.WOW64_64KEY
	case View32:
		return registry.READ | registry.WOW64_32KEY
	default:
		return registry.READ
	}
}

// Value 是读到的字符串值。
type Value struct {
	String string
	// Expand 为真表示该值的注册表类型是 REG_EXPAND_SZ。
	Expand bool
}

// Values 是一个键下批量读到的值。
type Values struct {
	Str   map[string]string
	DWORD map[string]uint32
}

// Reader 是注册表读取的最小接口，便于测试注入假实现。
type Reader interface {
	ReadString(hive Hive, keyPath, name string) (Value, error)
}

// Tree 提供子项遍历与视图控制，供软件清单采集使用。
type Tree interface {
	SubKeys(hive Hive, keyPath string, view View) ([]string, error)
	// ReadValues 一次打开键读取多个值，避免每个值都开关一次键。
	ReadValues(hive Hive, keyPath string, view View, strNames, dwordNames []string) (Values, error)
}

// Registry 是真实注册表实现，同时满足 Reader 与 Tree。
type Registry struct{}

// New 返回访问真实注册表的实例。
func New() *Registry { return &Registry{} }

func (r *Registry) ReadString(hive Hive, keyPath, name string) (Value, error) {
	k, err := registry.OpenKey(hive.root(), keyPath, registry.READ)
	if err != nil {
		return Value{}, fmt.Errorf("打开 %s\\%s 失败: %w", hive, keyPath, err)
	}
	defer k.Close()

	s, kind, err := k.GetStringValue(name)
	if err != nil {
		return Value{}, fmt.Errorf("读取 %s\\%s 的 %q 失败: %w", hive, keyPath, name, err)
	}
	return Value{String: s, Expand: kind == registry.EXPAND_SZ}, nil
}

func (r *Registry) SubKeys(hive Hive, keyPath string, view View) ([]string, error) {
	k, err := registry.OpenKey(hive.root(), keyPath, view.access())
	if err != nil {
		return nil, fmt.Errorf("打开 %s\\%s 失败: %w", hive, keyPath, err)
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("枚举 %s\\%s 子项失败: %w", hive, keyPath, err)
	}
	return names, nil
}

func (r *Registry) ReadValues(hive Hive, keyPath string, view View, strNames, dwordNames []string) (Values, error) {
	k, err := registry.OpenKey(hive.root(), keyPath, view.access())
	if err != nil {
		return Values{}, err
	}
	defer k.Close()

	out := Values{
		Str:   make(map[string]string, len(strNames)),
		DWORD: make(map[string]uint32, len(dwordNames)),
	}
	for _, n := range strNames {
		if s, _, err := k.GetStringValue(n); err == nil {
			out.Str[n] = s
		}
	}
	for _, n := range dwordNames {
		if v, _, err := k.GetIntegerValue(n); err == nil {
			out.DWORD[n] = uint32(v)
		}
	}
	return out, nil
}

// Package pathmgmt 负责 PATH 的**写入**与回滚。
//
// 为什么单独成包：internal/winreg 刻意不导出任何写操作，保证扫描阶段从类型层面
// 就写不了注册表。写入能力只在这里，只被 apply/rollback 使用。
package pathmgmt

import (
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// 注册表值类型（与 registry 包常量对齐，便于 JSON 里可读）。
const (
	KindSZ       uint32 = 1 // REG_SZ
	KindExpandSZ uint32 = 2 // REG_EXPAND_SZ
)

const (
	userPathKey    = `Environment`
	machinePathKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

// Entry 是一处 PATH 的原始值与类型。
type Entry struct {
	Value string `json:"value"`
	Kind  uint32 `json:"kind"`
}

// Snapshot 是两处 PATH 的完整快照（C3）。
type Snapshot struct {
	TakenAt string `json:"taken_at"`
	User    Entry  `json:"user"`
	Machine Entry  `json:"machine"`
}

// Manager 抽象 PATH 读写，便于测试注入假实现。
type Manager interface {
	Snapshot() (Snapshot, error)
	Current(hive string) (Entry, error)
	Write(hive string, e Entry) error
	// Notify 广播 WM_SETTINGCHANGE，让新启动的进程读到新 PATH。
	Notify() error
}

// Hive 名称。
const (
	HiveUser    = "HKCU"
	HiveMachine = "HKLM"
)

// Windows 是真实实现。
type Windows struct{}

// New 返回访问真实注册表的 Manager。
func New() Manager { return Windows{} }

func keyOf(hive string) (registry.Key, string) {
	if hive == HiveMachine {
		return registry.LOCAL_MACHINE, machinePathKey
	}
	return registry.CURRENT_USER, userPathKey
}

func (Windows) read(hive string) (Entry, error) {
	root, path := keyOf(hive)
	k, err := registry.OpenKey(root, path, registry.READ)
	if err != nil {
		return Entry{}, fmt.Errorf("打开 %s\\%s 失败: %w", hive, path, err)
	}
	defer k.Close()

	s, kind, err := k.GetStringValue("Path")
	if err != nil {
		if err == registry.ErrNotExist {
			return Entry{Kind: KindExpandSZ}, nil
		}
		return Entry{}, fmt.Errorf("读取 %s Path 失败: %w", hive, err)
	}
	if kind != KindSZ && kind != KindExpandSZ {
		kind = KindSZ
	}
	return Entry{Value: s, Kind: uint32(kind)}, nil
}

func (w Windows) Current(hive string) (Entry, error) { return w.read(hive) }

func (Windows) Snapshot() (Snapshot, error) {
	user, err := Windows{}.read(HiveUser)
	if err != nil {
		return Snapshot{}, err
	}
	machine, err := Windows{}.read(HiveMachine)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		TakenAt: time.Now().Format(time.RFC3339),
		User:    user,
		Machine: machine,
	}, nil
}

func (Windows) Write(hive string, e Entry) error {
	root, path := keyOf(hive)
	// 先读一次确认键存在（HKCU\Environment 一定存在，HKLM 也是）。
	k, err := registry.OpenKey(root, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 %s\\%s 写入失败（可能需要管理员权限）: %w", hive, path, err)
	}
	defer k.Close()

	// 值类型必须原样保留：REG_EXPAND_SZ 里的 %VAR% 交给系统展开，
	// 写成 REG_SZ 会让那些变量变成字面量（C3）。
	if e.Kind == KindExpandSZ {
		if err := k.SetExpandStringValue("Path", e.Value); err != nil {
			return fmt.Errorf("写入 %s Path（REG_EXPAND_SZ）失败: %w", hive, err)
		}
		return nil
	}
	if err := k.SetStringValue("Path", e.Value); err != nil {
		return fmt.Errorf("写入 %s Path（REG_SZ）失败: %w", hive, err)
	}
	return nil
}

// AppendMissing 把 entries 里尚未存在的条目追加到原值末尾，返回新值与被追加的条目。
//
// 只增不减、不重排已有条目（D11/§6.2）。
func AppendMissing(current string, entries []string) (string, []string) {
	existing := make(map[string]bool)
	for _, p := range strings.Split(current, ";") {
		existing[norm(p)] = true
	}

	added := make([]string, 0, len(entries))
	out := current
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" || existing[norm(e)] {
			continue
		}
		existing[norm(e)] = true
		added = append(added, e)
		if strings.TrimSpace(out) == "" {
			out = e
			continue
		}
		out = strings.TrimRight(out, ";") + ";" + e
	}
	return out, added
}

func norm(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	p = strings.TrimRight(p, `\`)
	return strings.ToLower(p)
}

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
)

// Notify 广播 WM_SETTINGCHANGE("Environment")。
//
// 为什么必须做：注册表改完后，已经运行的进程（资源管理器、各种 shell）不会自动
// 重新读环境变量，新启动的进程会从父进程继承旧值。广播这个消息后系统会刷新环境
// 块，新开的终端才拿得到新 PATH。
func (Windows) Notify() error {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	env, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	var result uintptr
	r1, _, callErr := procSendMessageTimeoutW.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung,
		5000,
		uintptr(unsafe.Pointer(&result)),
	)
	if r1 == 0 {
		return fmt.Errorf("广播 WM_SETTINGCHANGE 失败: %v", callErr)
	}
	return nil
}

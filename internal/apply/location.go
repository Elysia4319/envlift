package apply

import (
	"strings"

	"github.com/Elysia4319/envlift/internal/winreg"
)

// LocationReader 读取已安装软件的注册表 InstallLocation，供目录落点验证使用。
type LocationReader interface {
	InstallLocation(name string) (string, bool, error)
}

// RegistryLocationReader 从 Uninstall 键读取 InstallLocation。
type RegistryLocationReader struct {
	tree winreg.Tree
}

// NewRegistryLocationReader 返回真实注册表实现。
func NewRegistryLocationReader() *RegistryLocationReader {
	return &RegistryLocationReader{tree: winreg.New()}
}

const uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

var locationTargets = []struct {
	hive winreg.Hive
	view winreg.View
}{
	{winreg.HiveLocalMachine, winreg.View64},
	{winreg.HiveLocalMachine, winreg.View32},
	{winreg.HiveCurrentUser, winreg.ViewDefault},
}

// InstallLocation 按 DisplayName 精确（忽略大小写与首尾空白）匹配一条卸载记录。
func (r *RegistryLocationReader) InstallLocation(name string) (string, bool, error) {
	want := strings.TrimSpace(name)
	for _, t := range locationTargets {
		subs, err := r.tree.SubKeys(t.hive, uninstallKey, t.view)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			vals, err := r.tree.ReadValues(t.hive, uninstallKey+`\`+sub, t.view,
				[]string{"DisplayName", "InstallLocation"}, nil)
			if err != nil {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(vals.Str["DisplayName"]), want) {
				continue
			}
			return strings.TrimSpace(vals.Str["InstallLocation"]), true, nil
		}
	}
	return "", false, nil
}

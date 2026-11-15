package recipes

import "testing"

func TestBuiltinInstallDirCoverage(t *testing.T) {
	local := map[string]bool{
		"git": true, "vscode": true, "python": true, "7zip": true, "notepadplusplus": true,
	}
	winget := map[string]bool{
		"git": true, "vscode": true, "node": true, "python": true,
		"go": true, "7zip": true, "notepadplusplus": true, "pwsh": true,
	}
	seen := map[string]bool{}
	for _, r := range Builtin() {
		if local[r.Name] {
			seen[r.Name] = true
			if r.InstallDirArg == "" || r.InstallDirSource == "" {
				t.Errorf("%s 应声明本地安装目录参数与来源", r.Name)
			}
		}
		if winget[r.Name] {
			seen[r.Name] = true
			if r.WingetLocationArg == "" || r.InstallDirSource == "" {
				t.Errorf("%s 应声明 winget 目录参数与来源", r.Name)
			}
		}
	}
	for name := range local {
		if !seen[name] {
			t.Errorf("内置表缺少本地目录配方 %s", name)
		}
	}
	for name := range winget {
		if !seen[name] {
			t.Errorf("内置表缺少 winget 目录配方 %s", name)
		}
	}
}

package recipes

import "testing"

func TestBuiltinInstallDirCoverage(t *testing.T) {
	local := map[string]bool{
		"vscode": true, "python": true, "7zip": true,
	}
	winget := map[string]bool{
		"vscode": true, "node": true, "python": true,
		"go": true, "7zip": true, "pwsh": true,
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

func TestBuiltinNsisInstallDirArgUsesRestOfLine(t *testing.T) {
	for _, r := range Builtin() {
		if r.Name != "7zip" {
			continue
		}
		if r.InstallDirArg != `/D={install_dir}` {
			t.Errorf("%s 的 NSIS /D 参数应使用剩余整行模板，实际 %q", r.Name, r.InstallDirArg)
		}
	}
}

func TestUnverifiedBuiltinLocalDirIsDisabled(t *testing.T) {
	for _, r := range Builtin() {
		if r.Name != "git" && r.Name != "notepadplusplus" {
			continue
		}
		if r.InstallDirArg != "" || r.WingetLocationArg != "" || r.InstallDirSource != "" {
			t.Errorf("%s 的目录参数尚未验证，必须保持为空: %+v", r.Name, r)
		}
	}
}

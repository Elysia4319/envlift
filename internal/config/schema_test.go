package config

import (
	"strings"
	"testing"
)

func installerWithDirArgs(name, installDirArg, wingetArg string) Installer {
	return Installer{
		Name:              name,
		Match:             "Setup-*.exe",
		SilentArgs:        "/S",
		NeedsAdmin:        true,
		Source:            InstallerBuiltin,
		InstallDirArg:     installDirArg,
		WingetLocationArg: wingetArg,
		InstallDirSource:  "test source",
	}
}

func installerWithoutSource(name, installDirArg, wingetArg string) Installer {
	ins := installerWithDirArgs(name, installDirArg, wingetArg)
	ins.InstallDirSource = ""
	return ins
}

func TestValidateInstallDirTemplates(t *testing.T) {
	cases := []struct {
		name    string
		ins     Installer
		wantErr string
	}{
		{
			name: "both local and winget templates are valid",
			ins: installerWithDirArgs("ok", `/DIR="{install_dir}"`,
				`--location "{install_dir}"`),
		},
		{
			name:    "placeholder twice is rejected",
			ins:     installerWithDirArgs("twice", `/DIR="{install_dir}" /BACKUP="{install_dir}"`, ""),
			wantErr: "install_dir_arg",
		},
		{
			name:    "newline is rejected",
			ins:     installerWithDirArgs("newline", "/DIR={install_dir}\n/S", ""),
			wantErr: "换行",
		},
		{
			name:    "unbalanced double quote is rejected",
			ins:     installerWithDirArgs("quote", `/DIR="{install_dir}`, ""),
			wantErr: "双引号",
		},
		{
			name:    "control character is rejected",
			ins:     installerWithDirArgs("control", "/DIR={install_dir}\x01", ""),
			wantErr: "控制字符",
		},
		{
			name:    "source is required",
			ins:     installerWithoutSource("no-source", `/DIR="{install_dir}"`, ""),
			wantErr: "install_dir_source",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{App: AppName, Schema: CurrentSchema, Installers: []Installer{c.ins}}
			err := cfg.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应通过，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应拒绝，实际通过")
			}
			msg := err.Error()
			if !strings.Contains(msg, c.ins.Name) {
				t.Errorf("错误信息应包含配方名 %q: %s", c.ins.Name, msg)
			}
			if !strings.Contains(msg, c.wantErr) {
				t.Errorf("错误信息应包含 %q: %s", c.wantErr, msg)
			}
		})
	}
}

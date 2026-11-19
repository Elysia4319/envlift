package engine

import (
	"testing"

	"github.com/Elysia4319/envlift/internal/api"
	"github.com/Elysia4319/envlift/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestApplyCondaSettings(t *testing.T) {
	cfg := &config.Config{
		CondaEnvs: []config.CondaEnv{
			{Name: "py311", Manager: `C:\conda`},
			{Name: "py312", Manager: `C:\conda`},
		},
	}
	err := applyCondaSettings(cfg, []api.CondaSetting{{
		Name: "py311", Manager: `C:\conda`,
		RestoreConda: boolPtr(false), RestorePip: boolPtr(true), Mirror: "corp",
	}})
	if err != nil {
		t.Fatalf("applyCondaSettings: %v", err)
	}
	if cfg.CondaEnvs[0].CondaEnabled() || !cfg.CondaEnvs[0].PipEnabled() || cfg.CondaEnvs[0].Mirror != "corp" {
		t.Fatalf("开关未写回: %+v", cfg.CondaEnvs[0])
	}
	if cfg.CondaEnvs[1].RestoreConda != nil || cfg.CondaEnvs[1].RestorePip != nil {
		t.Fatalf("未指定的环境不应被改动: %+v", cfg.CondaEnvs[1])
	}
	if err := applyCondaSettings(cfg, []api.CondaSetting{{Name: "missing"}}); err == nil {
		t.Fatal("不存在的环境应报错")
	}
}

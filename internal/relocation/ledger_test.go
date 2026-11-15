package relocation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerRoundTripAndFind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultPath)
	l := New()
	l.Upsert(Entry{
		TargetID: "a1", InstallDir: `D:\Apps\Git\`, ActualDir: `D:\Apps\Git`,
		Verified: true,
	})
	if err := Save(path, l); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := got.Find("a1", `d:/apps/git`); !ok || !e.Verified || e.ActualDir != `D:\Apps\Git` {
		t.Fatalf("归一化查找失败: %+v / %v", e, ok)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsNewerLedgerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultPath)
	data := []byte("version: 2\nrelocations: []\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "账本版本高于本程序支持") {
		t.Fatalf("版本过高应拒绝读取: %v", err)
	}
}

func TestNormalizeDirPrefixesAndSlashes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`D:\Apps\Git\`, `d:\apps\git`},
		{`D:/Apps/Git`, `d:\apps\git`},
		{`\\?\D:\Apps\Git`, `d:\apps\git`},
		{`\\?\UNC\server\share\`, `\\server\share`},
	}
	for _, c := range cases {
		if got := NormalizeDir(c.in); got != c.want {
			t.Errorf("NormalizeDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadCorruptLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, []byte("version: [broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("损坏账本应返回错误")
	}
}

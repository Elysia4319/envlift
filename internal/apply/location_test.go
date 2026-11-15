package apply

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/winreg"
)

type fakeUninstallTree struct {
	subkeys map[string][]string
	values  map[string]winreg.Values
}

func fakeTreeKey(hive winreg.Hive, view winreg.View) string {
	return fmt.Sprintf("%d|%d", hive, view)
}

func fakeValueKey(hive winreg.Hive, view winreg.View, keyPath string) string {
	return fmt.Sprintf("%d|%d|%s", hive, view, keyPath)
}

func (f fakeUninstallTree) SubKeys(hive winreg.Hive, _ string, view winreg.View) ([]string, error) {
	return f.subkeys[fakeTreeKey(hive, view)], nil
}

func (f fakeUninstallTree) ReadValues(hive winreg.Hive, keyPath string, view winreg.View, _, _ []string) (winreg.Values, error) {
	v, ok := f.values[fakeValueKey(hive, view, keyPath)]
	if !ok {
		return winreg.Values{}, fmt.Errorf("找不到键 %s", keyPath)
	}
	return v, nil
}

func locationTree(locs map[winreg.Hive]map[winreg.View]string) fakeUninstallTree {
	tree := fakeUninstallTree{
		subkeys: map[string][]string{},
		values:  map[string]winreg.Values{},
	}
	for hive, byView := range locs {
		for view, loc := range byView {
			sub := "App"
			tree.subkeys[fakeTreeKey(hive, view)] = []string{sub}
			tree.values[fakeValueKey(hive, view, uninstallKey+`\`+sub)] = winreg.Values{
				Str: map[string]string{"DisplayName": "App", "InstallLocation": loc},
			}
		}
	}
	return tree
}

func TestInstallLocationsDedupesSamePath(t *testing.T) {
	r := &RegistryLocationReader{tree: locationTree(map[winreg.Hive]map[winreg.View]string{
		winreg.HiveLocalMachine: {winreg.View64: `D:\Apps\Git\`, winreg.View32: `d:/apps/git`},
		winreg.HiveCurrentUser:  {winreg.ViewDefault: `D:\Apps\Git`},
	})}
	got, err := r.InstallLocations("App")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != `D:\Apps\Git\` {
		t.Fatalf("同一路径应只留首个原始值: %#v", got)
	}
	loc, found, err := r.InstallLocation("App")
	if err != nil || !found || loc != `D:\Apps\Git\` {
		t.Fatalf("去重后应能唯一匹配: %q %v %v", loc, found, err)
	}
}

func TestInstallLocationsKeepsDistinctPathsAndVerifyIsUnverified(t *testing.T) {
	r := &RegistryLocationReader{tree: locationTree(map[winreg.Hive]map[winreg.View]string{
		winreg.HiveLocalMachine: {winreg.View64: `D:\Apps\Git`},
		winreg.HiveCurrentUser:  {winreg.ViewDefault: `C:\Apps\Git`},
	})}
	got, err := r.InstallLocations("App")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("不同路径应保留多个候选: %#v", got)
	}

	step := Step{}
	verifyInstallDir(Options{Locations: r, Out: io.Discard}, &step, plan.Item{
		Kind: "software", Name: "App", InstallDir: `D:\Apps\Git`,
	})
	if step.InstallDirVerified || !strings.Contains(step.InstallDirActual, " | ") {
		t.Fatalf("多候选应标 unverified 并列出候选: %+v", step)
	}
}

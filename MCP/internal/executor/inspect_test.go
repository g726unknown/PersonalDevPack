package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

// 本文件的用例一路真到底：真 pathobserver + 临时目录 home，
// 只有 pacman 侧挂着哑 stub（InspectPaths 不该碰它）。

func newInspectService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	rec := recorder.Open(t.TempDir())
	svc := New(observer.New(pacmanStub("", "", nil, "")), rec, pathobserver.New(home))
	return svc, home
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// 1. 临时 home 下建文件与目录 → facts 正确、零 warnings。
func TestInspectPathsFacts(t *testing.T) {
	svc, home := newInspectService(t)
	writeFile(t, home, "notes.txt", "hello")
	if err := os.Mkdir(filepath.Join(home, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(home, "sub"), "a.txt", "x")

	res, err := svc.InspectPaths(t.Context(), InspectInput{
		Paths: []string{"~/notes.txt", "~/sub"},
	})
	if err != nil {
		t.Fatalf("InspectPaths() error = %v", err)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(res.Entries))
	}

	f := res.Entries[0]
	if f.Path != "~/notes.txt" || !f.Exists || f.Kind != pathobserver.KindFile || f.Size != 5 {
		t.Errorf("file Facts = %+v", f)
	}
	d := res.Entries[1]
	if d.Kind != pathobserver.KindDir || d.EntriesTotal != 1 || len(d.EntriesSample) != 1 || d.EntriesSample[0] != "a.txt" {
		t.Errorf("dir Facts = %+v", d)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v, want empty", res.Warnings)
	}
}

// 2. floor 命中：~/.ssh/id_rsa → warning 提到它、SensitiveEntries 正确。
func TestInspectPathsFloorWarning(t *testing.T) {
	svc, home := newInspectService(t)
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, ssh, "id_rsa", "x")

	res, err := svc.InspectPaths(t.Context(), InspectInput{Paths: []string{"~/.ssh"}})
	if err != nil {
		t.Fatalf("InspectPaths() error = %v", err)
	}
	f := res.Entries[0]
	if len(f.SensitiveEntries) != 1 || f.SensitiveEntries[0] != "id_rsa" {
		t.Fatalf("SensitiveEntries = %v, want [id_rsa]", f.SensitiveEntries)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("len(Warnings) = %d, want 1: %v", len(res.Warnings), res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "id_rsa") || !strings.Contains(res.Warnings[0], "sensitive") {
		t.Errorf("warning %q 没有把私钥点名", res.Warnings[0])
	}
}

// 3. 无权限条目（父目录 chmod 000）→ 该条 warning、其余完好。
func TestInspectPathsPermissionWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位约束")
	}
	svc, home := newInspectService(t)
	writeFile(t, home, "ok.txt", "x")

	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.MkdirAll(filepath.Join(parent, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	res, err := svc.InspectPaths(t.Context(), InspectInput{
		Paths: []string{filepath.Join(parent, "sub"), "~/ok.txt"},
	})
	if err != nil {
		t.Fatalf("InspectPaths() error = %v（单条失败是 warning，不是整批失败）", err)
	}
	if res.Entries[0].Exists || res.Entries[0].Error == "" {
		t.Errorf("blocked Facts = %+v, want Error 非空 / Exists false", res.Entries[0])
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "probe failed") {
		t.Errorf("Warnings = %v, want 一条 probe failed", res.Warnings)
	}
	if !res.Entries[1].Exists || res.Entries[1].Error != "" {
		t.Errorf("healthy Facts = %+v, want 完好（单条失败不拖垮整批）", res.Entries[1])
	}
}

// 4. rejected：相对路径 / 空串 / ~user / 负 MaxEntries（表驱动）。
func TestInspectPathsRejected(t *testing.T) {
	svc, _ := newInspectService(t)
	cases := []struct {
		name string
		in   InspectInput
	}{
		{"相对路径", InspectInput{Paths: []string{"foo/bar"}}},
		{"空串", InspectInput{Paths: []string{"~/ok", ""}}},
		{"~user 形态", InspectInput{Paths: []string{"~otheruser/x"}}},
		{"负 MaxEntries", InspectInput{Paths: []string{"~/ok"}, MaxEntries: -1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.InspectPaths(t.Context(), c.in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("error = %v, want errors.Is(ErrInvalidInput)", err)
			}
		})
	}
}

// 5. 空 Paths → 空结果、无 error、不探查。
func TestInspectPathsEmptyInput(t *testing.T) {
	svc, home := newInspectService(t)

	res, err := svc.InspectPaths(t.Context(), InspectInput{})
	if err != nil {
		t.Fatalf("InspectPaths() error = %v, want nil（空请求合法）", err)
	}
	if res.Entries == nil || len(res.Entries) != 0 {
		t.Errorf("Entries = %+v, want 非 nil 空", res.Entries)
	}
	if res.Warnings == nil || len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v, want 非 nil 空", res.Warnings)
	}
	// home 里什么都没建，但空请求本来就不该碰它——这条只是防御性文档。
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("home 丢了？: %v", err)
	}
}

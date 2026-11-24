package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

// stubExec 与 observer 测试里的函数式样式一致：
// observer.Execer 是导出接口，executor 测试直接实现。
type stubExec struct {
	calls []string
	run   func(name string, args []string) ([]byte, []byte, error)
}

func (s *stubExec) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	return s.run(name, args)
}

// pacmanStub 按给定的 -Q / -Qm 输出应答。
func pacmanStub(qOut, qErr string, qExit error, mOut string) *stubExec {
	return &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		if name != "pacman" {
			return nil, nil, fmt.Errorf("unexpected command %q", name)
		}
		switch args[0] {
		case "-Q":
			return []byte(qOut), []byte(qErr), qExit
		case "-Qm":
			return []byte(mOut), nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected args %v", args)
		}
	}}
}

// newTestService 返回真 recorder（临时目录）+ 真 observer（挂 stub）。
// 三层真逻辑的组合，只把最外层的命令执行换成假货。
// pobs 挂临时 home：本文件的用例不探查路径，inspect 用例见 inspect_test.go。
func newTestService(t *testing.T, stub *stubExec) (*Service, *recorder.Recorder) {
	t.Helper()
	rec := recorder.Open(t.TempDir())
	return New(observer.New(stub), rec, pathobserver.New(t.TempDir())), rec
}

func TestAddPackageInstalled(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	res, err := svc.AddPackage(ctx, AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 0})
	if err != nil {
		t.Fatalf("AddPackage() error = %v, want nil", err)
	}
	if res.Action != recorder.ActionAdded {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionAdded)
	}
	if res.Revision != 1 {
		t.Errorf("Revision = %d, want 1", res.Revision)
	}
	if res.Warning != "" {
		t.Errorf("Warning = %q, want empty", res.Warning)
	}
	p := res.Package
	if p.Name != "neovim" || p.Version != "0.11.2" || p.Source != SourcePacman || p.Type != recorder.TypeDevTool {
		t.Errorf("Package = %+v", p)
	}

	// Load 重读盘：验证真的落了盘（真 recorder 临时目录）。
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if l.Revision != 1 {
		t.Errorf("on-disk Revision = %d, want 1", l.Revision)
	}
	dp := l.Packages["neovim"]
	if dp == nil {
		t.Fatal("Packages[\"neovim\"] missing on disk")
	}
	if dp.Version != "0.11.2" || dp.Source != SourcePacman || dp.Type != recorder.TypeDevTool {
		t.Errorf("on-disk package = %+v", *dp)
	}
	if dp.RecordedAt.IsZero() || dp.CheckedAt.IsZero() {
		t.Errorf("on-disk times zero: %+v", *dp)
	}
	if dp.RemovedAt != nil {
		t.Errorf("on-disk RemovedAt = %v, want nil", dp.RemovedAt)
	}
}

func TestAddPackageNotInstalled(t *testing.T) {
	stub := pacmanStub("", "error: package 'htop' was not found\n", errors.New("exit status 1"), "")
	svc, _ := newTestService(t, stub)

	res, err := svc.AddPackage(context.Background(), AddInput{Name: "htop", Type: recorder.TypeOther, BaseRevision: 0})
	if err != nil {
		t.Fatalf("AddPackage() error = %v, want nil", err)
	}
	if res.Action != recorder.ActionAdded {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionAdded)
	}
	if res.Package.Version != "" {
		t.Errorf("Version = %q, want empty", res.Package.Version)
	}
	if res.Package.Source != SourceUnknown {
		t.Errorf("Source = %q, want %q", res.Package.Source, SourceUnknown)
	}
	if res.Warning == "" {
		t.Error("Warning = empty, want non-empty for uninstalled package")
	}
}

func TestAddPackageForeign(t *testing.T) {
	stub := pacmanStub("vscodium 1.90.0\n", "", nil, "vscodium 1.90.0\n")
	svc, _ := newTestService(t, stub)

	res, err := svc.AddPackage(context.Background(), AddInput{Name: "vscodium", Type: recorder.TypeOther, BaseRevision: 0})
	if err != nil {
		t.Fatalf("AddPackage() error = %v, want nil", err)
	}
	if res.Package.Source != SourceAUR {
		t.Errorf("Source = %q, want %q", res.Package.Source, SourceAUR)
	}
}

func TestAddPackageSourceOverride(t *testing.T) {
	stub := pacmanStub("", "error: package 'foo' was not found\n", errors.New("exit status 1"), "")
	svc, _ := newTestService(t, stub)

	res, err := svc.AddPackage(context.Background(), AddInput{
		Name: "foo", Type: recorder.TypeOther, Source: SourceManual, BaseRevision: 0,
	})
	if err != nil {
		t.Fatalf("AddPackage() error = %v, want nil", err)
	}
	if res.Package.Source != SourceManual {
		t.Errorf("Source = %q, want %q (manual override)", res.Package.Source, SourceManual)
	}
}

func TestAddPackageStaleBaseRevConflicts(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	if _, err := svc.AddPackage(ctx, AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 0}); err != nil {
		t.Fatalf("first AddPackage() error = %v", err)
	}

	// baseRev 0 已过期：盘上是 1。
	_, err := svc.AddPackage(ctx, AddInput{Name: "htop", Type: recorder.TypeOther, BaseRevision: 0})
	if !errors.Is(err, recorder.ErrConflict) {
		t.Fatalf("AddPackage() error = %v, want recorder.ErrConflict", err)
	}

	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Revision != 1 {
		t.Errorf("on-disk Revision = %d, want 1 (unchanged)", l.Revision)
	}
	if _, ok := l.Packages["htop"]; ok {
		t.Error("htop was written despite conflict")
	}
}

func TestAddPackageUnchangedStillWrites(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	in := AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 0}
	if _, err := svc.AddPackage(ctx, in); err != nil {
		t.Fatalf("first AddPackage() error = %v", err)
	}

	// 相同输入再来一次：facts 与盘上完全一致 → unchanged，
	// 但 recorder 语义照常落盘、revision 照常 +1。
	in.BaseRevision = 1
	res, err := svc.AddPackage(ctx, in)
	if err != nil {
		t.Fatalf("second AddPackage() error = %v", err)
	}
	if res.Action != recorder.ActionUnchanged {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionUnchanged)
	}
	if res.Revision != 2 {
		t.Errorf("Revision = %d, want 2 (unchanged still writes)", res.Revision)
	}

	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Revision != 2 {
		t.Errorf("on-disk Revision = %d, want 2", l.Revision)
	}
}

func TestAddPackageInvalidTypeRejectedBeforeProbe(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)

	_, err := svc.AddPackage(context.Background(), AddInput{Name: "neovim", Type: "garbage", BaseRevision: 0})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("AddPackage() error = %v, want ErrInvalidInput", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("probe calls = %d, want 0 (rejection must not spend a subprocess): %v", len(stub.calls), stub.calls)
	}

	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Revision != 0 || len(l.Packages) != 0 {
		t.Errorf("disk changed on rejection: Revision=%d Packages=%d", l.Revision, len(l.Packages))
	}
}

func TestAddPackageEmptyNameRejected(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)

	_, err := svc.AddPackage(context.Background(), AddInput{Name: "", Type: recorder.TypeOther, BaseRevision: 0})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("AddPackage() error = %v, want ErrInvalidInput", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("probe calls = %d, want 0", len(stub.calls))
	}

	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Revision != 0 || len(l.Packages) != 0 {
		t.Errorf("disk changed on rejection: Revision=%d Packages=%d", l.Revision, len(l.Packages))
	}
}

func TestAddPackageAfterRemovalIsRestored(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, _ := newTestService(t, stub)
	ctx := context.Background()

	res1, err := svc.AddPackage(ctx, AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 0})
	if err != nil {
		t.Fatalf("first AddPackage() error = %v", err)
	}
	if _, err := svc.RemovePackage(ctx, RemoveInput{Name: "neovim", BaseRevision: 1}); err != nil {
		t.Fatalf("RemovePackage() error = %v", err)
	}

	res3, err := svc.AddPackage(ctx, AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 2})
	if err != nil {
		t.Fatalf("re-add AddPackage() error = %v", err)
	}
	if res3.Action != recorder.ActionRestored {
		t.Errorf("Action = %q, want %q", res3.Action, recorder.ActionRestored)
	}
	if res3.Package.RemovedAt != nil {
		t.Errorf("RemovedAt = %v, want nil", res3.Package.RemovedAt)
	}
	if !res3.Package.RecordedAt.Equal(res1.Package.RecordedAt) {
		t.Errorf("RecordedAt = %v, want first-recorded %v", res3.Package.RecordedAt, res1.Package.RecordedAt)
	}
}

func TestRemovePackageBranches(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, _ := newTestService(t, stub)
	ctx := context.Background()

	if _, err := svc.AddPackage(ctx, AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 0}); err != nil {
		t.Fatalf("seed AddPackage() error = %v", err)
	}
	stub.calls = nil // 下面只观察 Remove 是否探查

	res, err := svc.RemovePackage(ctx, RemoveInput{Name: "neovim", BaseRevision: 1})
	if err != nil {
		t.Fatalf("RemovePackage() error = %v, want nil", err)
	}
	if res.Action != recorder.ActionRemoved {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionRemoved)
	}
	if res.Revision != 2 {
		t.Errorf("Revision = %d, want 2", res.Revision)
	}
	if len(stub.calls) != 0 {
		t.Errorf("RemovePackage probed the system (%v), want no observer contact", stub.calls)
	}

	res, err = svc.RemovePackage(ctx, RemoveInput{Name: "neovim", BaseRevision: 2})
	if err != nil {
		t.Fatalf("second RemovePackage() error = %v, want nil", err)
	}
	if res.Action != recorder.ActionAlreadyRemoved {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionAlreadyRemoved)
	}
	if res.Revision != 3 {
		t.Errorf("Revision = %d, want 3 (recorder 语义：照常落盘、revision 照常 +1)", res.Revision)
	}

	res, err = svc.RemovePackage(ctx, RemoveInput{Name: "ghost", BaseRevision: 3})
	if err != nil {
		t.Fatalf("not_present must not be an error, got %v", err)
	}
	if res.Action != recorder.ActionNotPresent {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionNotPresent)
	}

	if _, err := svc.RemovePackage(ctx, RemoveInput{Name: "", BaseRevision: 4}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RemovePackage(empty name) error = %v, want ErrInvalidInput", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("empty-name RemovePackage probed the system: %v", stub.calls)
	}
}

func TestQueryPackages(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\nvscodium 1.90.0\n", "", nil, "vscodium 1.90.0\n")
	svc, _ := newTestService(t, stub)

	entries, missing, err := svc.QueryPackages(context.Background(), []string{"neovim", "vscodium", "htop"})
	if err != nil {
		t.Fatalf("QueryPackages() error = %v, want nil", err)
	}
	want := []PkgEntry{
		{Name: "neovim", Installed: true, Version: "0.11.2", Foreign: false, Source: SourcePacman},
		{Name: "vscodium", Installed: true, Version: "1.90.0", Foreign: true, Source: SourceAUR},
		{Name: "htop", Installed: false, Version: "", Foreign: false, Source: SourceUnknown},
	}
	if len(entries) != len(want) {
		t.Fatalf("len(entries) = %d, want %d (entries must cover all requested names)", len(entries), len(want))
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entries[%d] = %+v, want %+v", i, entries[i], want[i])
		}
	}
	if len(missing) != 1 || missing[0] != "htop" {
		t.Errorf("missing = %v, want [htop]", missing)
	}
	if len(stub.calls) != 2 {
		t.Errorf("exec calls = %d, want 2 (-Q 与 -Qm 各一次)", len(stub.calls))
	}
}

func TestQueryPackagesEmptyNameRejected(t *testing.T) {
	stub := pacmanStub("", "", nil, "")
	svc, _ := newTestService(t, stub)

	_, _, err := svc.QueryPackages(context.Background(), []string{"neovim", ""})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("QueryPackages() error = %v, want ErrInvalidInput", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("exec calls = %d, want 0", len(stub.calls))
	}
}

func TestQueryPackagesNoNamesRunsNothing(t *testing.T) {
	stub := pacmanStub("", "", nil, "")
	svc, _ := newTestService(t, stub)

	entries, missing, err := svc.QueryPackages(context.Background(), nil)
	if err != nil {
		t.Fatalf("QueryPackages(nil) error = %v, want nil", err)
	}
	if len(entries) != 0 || len(missing) != 0 {
		t.Errorf("entries=%d missing=%d, want 0/0", len(entries), len(missing))
	}
	if len(stub.calls) != 0 {
		t.Errorf("exec calls = %d, want 0", len(stub.calls))
	}
}

func TestAddPackageObserverErrorPassesThrough(t *testing.T) {
	// v1 假定目标环境有 pacman：探查失败原样透传，盘上无变化。
	// （无 pacman 机器的行为已记入 DESIGN 待定。）
	stub := &stubExec{run: func(name string, _ []string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("exec %s: %w", name, exec.ErrNotFound)
	}}
	svc, rec := newTestService(t, stub)

	_, err := svc.AddPackage(context.Background(), AddInput{Name: "neovim", Type: recorder.TypeDevTool, BaseRevision: 0})
	if err == nil {
		t.Fatal("AddPackage() error = nil, want passthrough error")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("errors.Is(err, exec.ErrNotFound) = false, err = %v", err)
	}

	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Revision != 0 || len(l.Packages) != 0 {
		t.Errorf("disk changed after probe failure: Revision=%d Packages=%d", l.Revision, len(l.Packages))
	}
}

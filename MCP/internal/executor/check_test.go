package executor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
)

var checkSeedTime = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

// seedEntry 直接种清单：CheckList 的用例需要构造“记录版 ≠ 实际版”
// 等状态，AddPackage 的完整写路径做不到（它记录的就是探查结果），
// 所以直接用 recorder 真实现（临时目录）种数据。
func seedEntry(t *testing.T, rec *recorder.Recorder, name, version, source string) {
	t.Helper()
	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = rec.Update(context.Background(), l.Revision, func(li *recorder.List) error {
		_, _, uerr := li.Upsert(name,
			recorder.Facts{Type: recorder.TypeOther, Version: version, Source: source}, checkSeedTime)
		return uerr
	})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestCheckListMixedStates(t *testing.T) {
	stub := &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "-Q":
			return []byte("neovim 0.11.2\nhtop 3.5.0\n"), nil, nil
		case "-Qm":
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected args %v", args)
	}}
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	seedEntry(t, rec, "neovim", "0.11.2", SourcePacman) // 记录 = 实际
	seedEntry(t, rec, "htop", "3.4.0", SourcePacman)    // 记录旧于实际
	seedEntry(t, rec, "ghost", "1.0", SourceUnknown)    // 记录有、系统没有

	res, err := svc.CheckList(ctx)
	if err != nil {
		t.Fatalf("CheckList() error = %v, want nil", err)
	}
	if res.Revision != 3 {
		t.Errorf("Revision = %d, want 3", res.Revision)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("len(Entries) = %d, want 3", len(res.Entries))
	}

	// 名字排序后顺序确定：ghost, htop, neovim。
	// CheckEntry 没有判定字段可断言——类型本身就是“报事实不判定”
	// 的保证，这里只能逐字段核事实。
	got := res.Entries[0]
	if got.Name != "ghost" || got.RecordedVersion != "1.0" || got.Installed ||
		got.ActualVersion != "" || got.RecordedSource != SourceUnknown || got.ActualSource != SourceUnknown {
		t.Errorf("ghost = %+v", got)
	}
	got = res.Entries[1]
	if got.Name != "htop" || got.RecordedVersion != "3.4.0" || !got.Installed ||
		got.ActualVersion != "3.5.0" || got.RecordedSource != SourcePacman || got.ActualSource != SourcePacman {
		t.Errorf("htop = %+v", got)
	}
	got = res.Entries[2]
	if got.Name != "neovim" || got.RecordedVersion != "0.11.2" || !got.Installed ||
		got.ActualVersion != "0.11.2" || got.RecordedSource != SourcePacman || got.ActualSource != SourcePacman {
		t.Errorf("neovim = %+v", got)
	}
}

func TestCheckListManualRecordedVersusPacmanActual(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)

	seedEntry(t, rec, "neovim", "0.11.2", SourceManual)

	res, err := svc.CheckList(context.Background())
	if err != nil {
		t.Fatalf("CheckList() error = %v, want nil", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(res.Entries))
	}
	e := res.Entries[0]
	// manual 是调用方当时的声明，pacman 是今天的探查：并列呈现，互不覆盖。
	if e.RecordedSource != SourceManual {
		t.Errorf("RecordedSource = %q, want %q", e.RecordedSource, SourceManual)
	}
	if e.ActualSource != SourcePacman {
		t.Errorf("ActualSource = %q, want %q", e.ActualSource, SourcePacman)
	}
}

func TestCheckListEmptyListRunsNothing(t *testing.T) {
	stub := pacmanStub("", "", nil, "") // 不应被调用
	svc, _ := newTestService(t, stub)

	res, err := svc.CheckList(context.Background())
	if err != nil {
		t.Fatalf("CheckList() error = %v, want nil", err)
	}
	if res.Revision != 0 {
		t.Errorf("Revision = %d, want 0", res.Revision)
	}
	if res.Entries == nil {
		t.Fatal("Entries = nil, want non-nil empty slice")
	}
	if len(res.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(res.Entries))
	}
	if len(stub.calls) != 0 {
		t.Errorf("exec calls = %d, want 0: %v", len(stub.calls), stub.calls)
	}
}

func TestCheckListIsReadOnly(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	seedEntry(t, rec, "neovim", "0.11.2", SourcePacman)
	before, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if _, err := svc.CheckList(ctx); err != nil {
		t.Fatalf("CheckList() error = %v", err)
	}

	after, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if before.Revision != after.Revision {
		t.Errorf("Revision changed: %d -> %d", before.Revision, after.Revision)
	}
	if !before.UpdatedAt.Equal(after.UpdatedAt) {
		t.Errorf("UpdatedAt changed: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestCheckListSkipsRemovedEntries(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\n", "", nil, "")
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	seedEntry(t, rec, "neovim", "0.11.2", SourcePacman)
	seedEntry(t, rec, "ghost", "1.0", SourceUnknown)
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(ctx, l.Revision, func(li *recorder.List) error {
		li.MarkRemoved("ghost", checkSeedTime)
		return nil
	}); err != nil {
		t.Fatalf("remove ghost: %v", err)
	}

	res, err := svc.CheckList(ctx)
	if err != nil {
		t.Fatalf("CheckList() error = %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1 (removed must be skipped)", len(res.Entries))
	}
	if res.Entries[0].Name != "neovim" {
		t.Errorf("Entries[0].Name = %q, want neovim", res.Entries[0].Name)
	}
}

func TestCheckListActualSourceValues(t *testing.T) {
	stub := &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "-Q":
			return []byte("neovim 0.11.2\nvscodium 1.90.0\n"), nil, nil
		case "-Qm":
			return []byte("vscodium 1.90.0\n"), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected args %v", args)
	}}
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	seedEntry(t, rec, "neovim", "0.11.2", SourcePacman)
	seedEntry(t, rec, "vscodium", "1.90.0", SourceAUR)
	seedEntry(t, rec, "ghost", "1.0", SourceUnknown)

	res, err := svc.CheckList(ctx)
	if err != nil {
		t.Fatalf("CheckList() error = %v", err)
	}
	byName := map[string]CheckEntry{}
	for _, e := range res.Entries {
		byName[e.Name] = e
	}
	if byName["neovim"].ActualSource != SourcePacman {
		t.Errorf("neovim.ActualSource = %q, want %q", byName["neovim"].ActualSource, SourcePacman)
	}
	if byName["vscodium"].ActualSource != SourceAUR {
		t.Errorf("vscodium.ActualSource = %q, want %q", byName["vscodium"].ActualSource, SourceAUR)
	}
	if byName["ghost"].ActualSource != SourceUnknown {
		t.Errorf("ghost.ActualSource = %q, want %q", byName["ghost"].ActualSource, SourceUnknown)
	}
}

func TestCheckListProbeErrorPassesThrough(t *testing.T) {
	stub := &stubExec{run: func(name string, _ []string) ([]byte, []byte, error) {
		return nil, []byte("error: database 'sync' is inconsistent\n"), errors.New("exit status 1")
	}}
	svc, rec := newTestService(t, stub)

	seedEntry(t, rec, "neovim", "0.11.2", SourcePacman)

	if _, err := svc.CheckList(context.Background()); err == nil {
		t.Fatal("CheckList() error = nil, want passthrough error")
	}
}

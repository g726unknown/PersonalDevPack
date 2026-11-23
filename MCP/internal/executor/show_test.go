package executor

import (
	"context"
	"testing"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
)

var showSeedTime = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func seedWithType(t *testing.T, rec *recorder.Recorder, name, typ string) {
	t.Helper()
	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = rec.Update(context.Background(), l.Revision, func(li *recorder.List) error {
		_, _, uerr := li.Upsert(name,
			recorder.Facts{Type: typ, Version: "1.0", Source: SourcePacman}, showSeedTime)
		return uerr
	})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestShowList(t *testing.T) {
	stub := pacmanStub("", "", nil, "") // 不应被调用
	svc, rec := newTestService(t, stub)
	ctx := context.Background()

	seedWithType(t, rec, "neovim", recorder.TypeDevTool)
	seedWithType(t, rec, "go", recorder.TypeRuntime)
	seedWithType(t, rec, "mariadb", recorder.TypeService)
	seedWithType(t, rec, "vim", recorder.TypeDevTool)

	// 软删 mariadb：计入 RemovedCount、不进 Entries 与 ByType。
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(ctx, l.Revision, func(li *recorder.List) error {
		li.MarkRemoved("mariadb", showSeedTime)
		return nil
	}); err != nil {
		t.Fatalf("remove mariadb: %v", err)
	}

	res, err := svc.ShowList(ctx)
	if err != nil {
		t.Fatalf("ShowList() error = %v, want nil", err)
	}
	if res.Revision != 5 {
		t.Errorf("Revision = %d, want 5", res.Revision)
	}
	if res.Total != 3 {
		t.Errorf("Total = %d, want 3 (removed not counted)", res.Total)
	}
	if res.RemovedCount != 1 {
		t.Errorf("RemovedCount = %d, want 1", res.RemovedCount)
	}
	if res.ByType[recorder.TypeDevTool] != 2 {
		t.Errorf("ByType[devtool] = %d, want 2", res.ByType[recorder.TypeDevTool])
	}
	if res.ByType[recorder.TypeRuntime] != 1 {
		t.Errorf("ByType[runtime] = %d, want 1", res.ByType[recorder.TypeRuntime])
	}
	if _, ok := res.ByType[recorder.TypeService]; ok {
		t.Errorf("removed entry's type leaked into ByType: %v", res.ByType)
	}

	// 名字升序：go, neovim, vim。
	wantOrder := []string{"go", "neovim", "vim"}
	if len(res.Entries) != len(wantOrder) {
		t.Fatalf("len(Entries) = %d, want %d", len(res.Entries), len(wantOrder))
	}
	for i, want := range wantOrder {
		if res.Entries[i].Name != want {
			t.Errorf("Entries[%d].Name = %q, want %q (ascending order)", i, res.Entries[i].Name, want)
		}
	}
	if len(stub.calls) != 0 {
		t.Errorf("ShowList probed the system: %v", stub.calls)
	}
}

func TestShowListEmptyList(t *testing.T) {
	stub := pacmanStub("", "", nil, "") // 不应被调用
	svc, _ := newTestService(t, stub)

	res, err := svc.ShowList(context.Background())
	if err != nil {
		t.Fatalf("ShowList() error = %v, want nil", err)
	}
	if res.Revision != 0 {
		t.Errorf("Revision = %d, want 0", res.Revision)
	}
	if res.Total != 0 {
		t.Errorf("Total = %d, want 0", res.Total)
	}
	if res.RemovedCount != 0 {
		t.Errorf("RemovedCount = %d, want 0", res.RemovedCount)
	}
	if res.Entries == nil {
		t.Fatal("Entries = nil, want non-nil empty slice")
	}
	if len(res.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(res.Entries))
	}
	if res.ByType == nil {
		t.Fatal("ByType = nil, want non-nil empty map")
	}
	if len(res.ByType) != 0 {
		t.Errorf("len(ByType) = %d, want 0", len(res.ByType))
	}
	if len(stub.calls) != 0 {
		t.Errorf("exec calls = %d, want 0", len(stub.calls))
	}
}

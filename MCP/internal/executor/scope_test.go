package executor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

// guardExec 是负向断言：SetScope 不探查——谁敢碰命令就当场失败，
// 把“存的是对话结果，不是系统事实”钉进测试。
type guardExec struct{}

func (guardExec) Run(context.Context, string, ...string) ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("unexpected command call")
}

func newScopeService(t *testing.T) (*Service, *recorder.Recorder) {
	t.Helper()
	rec := recorder.Open(t.TempDir())
	svc := New(observer.New(guardExec{}), rec, pathobserver.New(t.TempDir()))
	return svc, rec
}

// scopeSeed 往真 recorder 里种一个正常条目（真 Load + 真 Update）。
func scopeSeed(t *testing.T, rec *recorder.Recorder, name string) {
	t.Helper()
	ctx := context.Background()
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(ctx, l.Revision, func(li *recorder.List) error {
		_, _, uerr := li.Upsert(name,
			recorder.Facts{Type: recorder.TypeDevTool, Version: "1.0", Source: "pacman"}, time.Now())
		return uerr
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// 6. SetScope 成功 → ActionSet、revision+1、DecidedAt 就位；
// 回显是拷贝——改回显再 Load，盘未受影响。
func TestSetScopeSuccess(t *testing.T) {
	svc, rec := newScopeService(t)
	scopeSeed(t, rec, "neovim")
	ctx := t.Context()

	sc := recorder.Scope{
		Include: true,
		Reason:  "user confirmed config is worth keeping",
		Paths: []recorder.ScopePath{
			{Path: "~/.config/nvim", Action: recorder.ActionInclude, Reason: "main config"},
		},
	}
	res, err := svc.SetScope(ctx, ScopeInput{Name: "neovim", Scope: sc, BaseRevision: 1})
	if err != nil {
		t.Fatalf("SetScope() error = %v", err)
	}
	if res.Action != recorder.ActionSet {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionSet)
	}
	if res.Revision != 2 {
		t.Errorf("Revision = %d, want 2（revision+1）", res.Revision)
	}
	if res.Scope.DecidedAt.IsZero() {
		t.Error("DecidedAt = zero, want 就位")
	}
	if res.Scope.Include != true || len(res.Scope.Paths) != 1 {
		t.Errorf("回显 Scope = %+v", res.Scope)
	}

	// 回显是拷贝：篡改它，盘不受影响。
	res.Scope.Paths[0].Reason = "tampered"
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := l.Packages["neovim"].Config.Paths[0].Reason; got != "main config" {
		t.Errorf("盘上 reason = %q, want 原值 main config（回显是拷贝）", got)
	}
}

// 7. 过期 base_revision → errors.Is(recorder.ErrConflict)、盘未变。
func TestSetScopeConflict(t *testing.T) {
	svc, rec := newScopeService(t)
	scopeSeed(t, rec, "neovim") // revision 现在是 1

	_, err := svc.SetScope(t.Context(), ScopeInput{
		Name:         "neovim",
		Scope:        recorder.Scope{Include: false, Reason: "r"},
		BaseRevision: 0, // 过期
	})
	if !errors.Is(err, recorder.ErrConflict) {
		t.Fatalf("error = %v, want errors.Is(ErrConflict)", err)
	}
	l, err := rec.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Packages["neovim"].Config != nil {
		t.Error("conflict 后盘上出现 Config, want 未写")
	}
}

// 8. 校验失败（缺 reason）→ errors.Is(recorder.ErrInvalidScope)。
func TestSetScopeValidationFailed(t *testing.T) {
	svc, rec := newScopeService(t)
	scopeSeed(t, rec, "neovim")

	_, err := svc.SetScope(t.Context(), ScopeInput{
		Name:         "neovim",
		Scope:        recorder.Scope{Include: false}, // 缺顶层 reason
		BaseRevision: 1,
	})
	if !errors.Is(err, recorder.ErrInvalidScope) {
		t.Fatalf("error = %v, want errors.Is(ErrInvalidScope)", err)
	}
}

// 9. 软删条目 → 同哨兵（DESIGN 定它为 rejected：先恢复条目再谈范围）。
func TestSetScopeSoftRemoved(t *testing.T) {
	svc, rec := newScopeService(t)
	ctx := t.Context()
	scopeSeed(t, rec, "ghost")
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(ctx, l.Revision, func(li *recorder.List) error {
		li.MarkRemoved("ghost", time.Now())
		return nil
	}); err != nil {
		t.Fatalf("soft-remove: %v", err)
	}

	_, err = svc.SetScope(ctx, ScopeInput{
		Name:         "ghost",
		Scope:        recorder.Scope{Include: false, Reason: "r"},
		BaseRevision: 2,
	})
	if !errors.Is(err, recorder.ErrInvalidScope) {
		t.Fatalf("error = %v, want errors.Is(ErrInvalidScope)（软删条目是 rejected）", err)
	}
}

// 10. 不存在条目 → ActionNotPresent，无 error（handler 映射 not_found）。
func TestSetScopeNotPresent(t *testing.T) {
	svc, rec := newScopeService(t)

	res, err := svc.SetScope(t.Context(), ScopeInput{
		Name:         "ghost",
		Scope:        recorder.Scope{Include: false, Reason: "r"},
		BaseRevision: 0,
	})
	if err != nil {
		t.Fatalf("SetScope() error = %v, want nil（not_present 是业务结果）", err)
	}
	if res.Action != recorder.ActionNotPresent {
		t.Errorf("Action = %q, want %q", res.Action, recorder.ActionNotPresent)
	}
	if res.Revision != 1 {
		t.Errorf("Revision = %d, want 1（recorder 语义：not_present 也照常落盘）", res.Revision)
	}
	if res.Scope.Include || !res.Scope.DecidedAt.IsZero() {
		t.Errorf("Scope = %+v, want 零值（没有条目就没有存档）", res.Scope)
	}
	if _, err := rec.Load(t.Context()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// 顺带钉住空 name → ErrInvalidInput。
	if _, err := svc.SetScope(t.Context(), ScopeInput{Name: "", BaseRevision: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty name error = %v, want errors.Is(ErrInvalidInput)", err)
	}
}

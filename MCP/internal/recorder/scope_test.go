package recorder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件测 Scope 存档与 SetScope。除 Load 兼容性两条需要真临时目录，
// 其余全部纯内存（同 list_test.go 的约定）；错误路径统一用
// assertNoSideEffect 断言零副作用。

var (
	scT0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	scT1 = scT0.Add(time.Hour)
	scT2 = scT0.Add(2 * time.Hour)
	scT3 = scT0.Add(3 * time.Hour)
)

// seedEntry 往内存清单种一个正常条目，返回真身指针。
func seedEntry(t *testing.T, l *List, name string) *Package {
	t.Helper()
	_, p, err := l.Upsert(name, Facts{Type: TypeDevTool, Version: "1.0", Source: "pacman"}, scT0)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return p
}

// assertNoSideEffect 打下序列化快照、跑 fn（预期返回 error）、逐字节比对。
// json.Marshal 对 map 键排序，快照确定性；时间戳与 Config 都在快照里，
// 任何一处被误改都逃不过。
func assertNoSideEffect(t *testing.T, l *List, fn func() error) {
	t.Helper()
	before, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	if err := fn(); err == nil {
		t.Fatal("fn() error = nil, want error")
	}
	after, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("error path had side effects\nbefore: %s\nafter:  %s", before, after)
	}
}

// nvScope 是一个结构完备的范围判断：一墙一洞。
func nvScope() Scope {
	return Scope{
		Include: true,
		Reason:  "user confirmed config is worth keeping",
		Paths: []ScopePath{
			{Path: "~/.config/nvim", Action: ActionInclude, Reason: "main config, user confirmed"},
			{Path: "~/.config/nvim/.env", Action: ActionExclude, Reason: "may contain tokens, user chose to drop"},
		},
	}
}

// 1. 阶段一形态的 list.json（无 config 键）→ Load 后 Config 为 nil。
// 磁盘上那份真实清单就是这条测试的现实对应物——版本保持 1 的
// 兼容承诺在此兑现。
func TestLoadLegacyListHasNilConfig(t *testing.T) {
	dir := t.TempDir()
	fixture := `{
  "version": 1,
  "revision": 1,
  "updated_at": "2026-11-23T10:00:00Z",
  "packages": {
    "neovim": {
      "name": "neovim",
      "source": "pacman",
      "version": "0.11.2",
      "type": "devtool",
      "recorded_at": "2026-11-23T10:00:00Z",
      "checked_at": "2026-11-23T10:00:00Z"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "list.json"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	l, err := Open(dir).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v (阶段一形态必须继续可读)", err)
	}
	p := l.Packages["neovim"]
	if p == nil {
		t.Fatal("neovim missing from legacy list")
	}
	if p.Config != nil {
		t.Errorf("Config = %+v, want nil（尚未讨论过 ≠ 明确不备份）", p.Config)
	}
}

// 2. SetScope 成功（include + 一墙一洞）→ ActionSet，落盘往返保留。
func TestSetScopeIncludeWithExcludeRoundTrip(t *testing.T) {
	rec := Open(t.TempDir())
	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	l, err = rec.Update(context.Background(), l.Revision, func(li *List) error {
		_, _, uerr := li.Upsert("neovim", Facts{Type: TypeDevTool, Version: "0.11.2", Source: "pacman"}, scT0)
		return uerr
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// SetScope 在 Update 的 fn 里跑——真实调用链的形状。
	var act Action
	saved, err := rec.Update(context.Background(), l.Revision, func(li *List) error {
		var serr error
		act, serr = li.SetScope("neovim", nvScope(), scT1)
		return serr
	})
	if err != nil {
		t.Fatalf("Update(SetScope) error = %v", err)
	}
	if act != ActionSet {
		t.Errorf("Action = %q, want %q", act, ActionSet)
	}

	// 内存断言。
	p := saved.Packages["neovim"]
	if p.Config == nil {
		t.Fatal("Config = nil after SetScope")
	}
	if !p.Config.Include || p.Config.Reason != nvScope().Reason {
		t.Errorf("Config = %+v", p.Config)
	}
	if len(p.Config.Paths) != 2 {
		t.Fatalf("len(Paths) = %d, want 2", len(p.Config.Paths))
	}
	if p.Config.Paths[1].Action != ActionExclude {
		t.Errorf("Paths[1].Action = %q, want %q", p.Config.Paths[1].Action, ActionExclude)
	}

	// 真正的往返：重读，存档原样。
	re, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	rp := re.Packages["neovim"]
	if rp.Config == nil {
		t.Fatal("Config = nil after reload (存档没有落盘)")
	}
	if !rp.Config.Include || rp.Config.Reason != nvScope().Reason || len(rp.Config.Paths) != 2 {
		t.Errorf("reloaded Config = %+v", rp.Config)
	}
	if rp.Config.Paths[0].Path != "~/.config/nvim" ||
		rp.Config.Paths[0].Action != ActionInclude || rp.Config.Paths[0].Reason == "" {
		t.Errorf("reloaded Paths[0] = %+v", rp.Config.Paths[0])
	}
	if rp.Config.Paths[1].Path != "~/.config/nvim/.env" ||
		rp.Config.Paths[1].Action != ActionExclude || rp.Config.Paths[1].Reason == "" {
		t.Errorf("reloaded Paths[1] = %+v", rp.Config.Paths[1])
	}
	if !rp.Config.DecidedAt.Equal(scT1) {
		t.Errorf("reloaded DecidedAt = %v, want %v", rp.Config.DecidedAt, scT1)
	}
}

// 3. include:false 带顶层 reason → 成功且只存判断。
func TestSetScopeExplicitNoBackup(t *testing.T) {
	l := newList()
	seedEntry(t, l, "htop")

	act, err := l.SetScope("htop", Scope{Include: false, Reason: "user says htop config is throwaway"}, scT1)
	if err != nil {
		t.Fatalf("SetScope() error = %v", err)
	}
	if act != ActionSet {
		t.Errorf("Action = %q, want %q", act, ActionSet)
	}
	p := l.Packages["htop"]
	if p.Config == nil || p.Config.Include {
		t.Errorf("Config = %+v, want non-nil include:false", p.Config)
	}
	if p.Config.Reason != "user says htop config is throwaway" {
		t.Errorf("Reason = %q", p.Config.Reason)
	}
	if p.Config.Paths != nil {
		t.Errorf("Paths = %v, want nil（不备份只存判断）", p.Config.Paths)
	}
	if !p.Config.DecidedAt.Equal(scT1) {
		t.Errorf("DecidedAt = %v, want %v（明确不备份也是判断）", p.Config.DecidedAt, scT1)
	}
}

// DecidedAt 刷新：判断是“最新一次对话的结果”，每次 SetScope 成功
// 重写为新 now；失败的 SetScope 不写时间戳（零副作用快照顺带钉住）。
func TestSetScopeDecidedAtRefresh(t *testing.T) {
	l := newList()
	seedEntry(t, l, "neovim")

	if _, err := l.SetScope("neovim", nvScope(), scT1); err != nil {
		t.Fatalf("first SetScope() error = %v", err)
	}
	p := l.Packages["neovim"]
	if !p.Config.DecidedAt.Equal(scT1) {
		t.Errorf("DecidedAt = %v, want %v", p.Config.DecidedAt, scT1)
	}

	// 二次判断（意见反转：明确不备份）→ 刷新为新 now。
	if _, err := l.SetScope("neovim", Scope{Include: false, Reason: "user changed mind"}, scT2); err != nil {
		t.Fatalf("second SetScope() error = %v", err)
	}
	if !p.Config.DecidedAt.Equal(scT2) {
		t.Errorf("DecidedAt = %v, want refreshed %v (最新一次对话的结果)", p.Config.DecidedAt, scT2)
	}
	if p.Config.Include {
		t.Error("Include = true, want false (二次判断全量替换)")
	}

	// 失败的 SetScope 不写时间戳：快照含 DecidedAt，零副作用顺带钉住。
	assertNoSideEffect(t, l, func() error {
		_, err := l.SetScope("neovim", Scope{Include: false}, scT3) // 缺 reason → error
		return err
	})
	if !p.Config.DecidedAt.Equal(scT2) {
		t.Errorf("DecidedAt after failed set = %v, want untouched %v", p.Config.DecidedAt, scT2)
	}
}

// 4. include:false 缺顶层 reason → error，零副作用。
func TestSetScopeNoBackupWithoutReason(t *testing.T) {
	l := newList()
	seedEntry(t, l, "htop")

	assertNoSideEffect(t, l, func() error {
		_, err := l.SetScope("htop", Scope{Include: false}, scT1)
		return err
	})
}

// 5. include:true 但一条 include 路径都没有 → error（备份空集）。
func TestSetScopeIncludeWithoutIncludePath(t *testing.T) {
	l := newList()
	seedEntry(t, l, "neovim")

	cases := []struct {
		name  string
		paths []ScopePath
	}{
		{"nil paths", nil},
		{"只有 exclude", []ScopePath{
			{Path: "~/.config/nvim", Action: ActionExclude, Reason: "r"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertNoSideEffect(t, l, func() error {
				_, err := l.SetScope("neovim", Scope{Include: true, Paths: c.paths}, scT1)
				return err
			})
		})
	}
}

// 6. 相对路径 → error。
func TestSetScopeRelativePath(t *testing.T) {
	l := newList()
	seedEntry(t, l, "neovim")

	for _, bad := range []string{"config/nvim", "./config", "../escape"} {
		assertNoSideEffect(t, l, func() error {
			_, err := l.SetScope("neovim", Scope{
				Include: true,
				Paths:   []ScopePath{{Path: bad, Action: ActionInclude, Reason: "r"}},
			}, scT1)
			return err
		})
	}
}

// 7. 空 path / 非法 action / 空 reason → error（表驱动）。
func TestSetScopeInvalidPathFields(t *testing.T) {
	l := newList()
	seedEntry(t, l, "neovim")

	cases := []struct {
		name string
		sp   ScopePath
	}{
		{"空 path", ScopePath{Path: "", Action: ActionInclude, Reason: "r"}},
		{"非法 action", ScopePath{Path: "~/.config/nvim", Action: "wall", Reason: "r"}},
		{"空 reason", ScopePath{Path: "~/.config/nvim", Action: ActionInclude, Reason: ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertNoSideEffect(t, l, func() error {
				_, err := l.SetScope("neovim", Scope{
					Include: true,
					Paths: []ScopePath{
						{Path: "~/.config/nvim", Action: ActionInclude, Reason: "wall"},
						c.sp,
					},
				}, scT1)
				return err
			})
		})
	}
}

// 8. exclude 无墙可依 → error；兄弟前缀（/a/b vs /a/bc）同样不算在墙里。
func TestSetScopeExcludeOutsideWall(t *testing.T) {
	l := newList()
	seedEntry(t, l, "app")

	cases := []struct {
		name string
		wall string
		hole string
	}{
		{"完全不相干", "/etc/app", "~/.zshrc"},
		{"兄弟前缀不带分隔符", "/a/b", "/a/bc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertNoSideEffect(t, l, func() error {
				_, err := l.SetScope("app", Scope{
					Include: true,
					Paths: []ScopePath{
						{Path: c.wall, Action: ActionInclude, Reason: "wall"},
						{Path: c.hole, Action: ActionExclude, Reason: "hole"},
					},
				}, scT1)
				return err
			})
		})
	}
}

// 9. exclude 等于 include 本身 → error。同一条路径既是墙又是洞是歧义，
// 先被重复检查拦下（Clean 后同一键出现两次）——拒绝的理由不同族，
// 拒绝的事实相同。
func TestSetScopeExcludeEqualsInclude(t *testing.T) {
	l := newList()
	seedEntry(t, l, "app")

	assertNoSideEffect(t, l, func() error {
		_, err := l.SetScope("app", Scope{
			Include: true,
			Paths: []ScopePath{
				{Path: "/a", Action: ActionInclude, Reason: "wall"},
				{Path: "/a", Action: ActionExclude, Reason: "hole"},
			},
		}, scT1)
		return err
	})
}

// 10. 重复 path（Clean 后同键）→ error。
func TestSetScopeDuplicatePath(t *testing.T) {
	l := newList()
	seedEntry(t, l, "app")

	cases := []struct {
		name  string
		paths []ScopePath
	}{
		{"字面重复", []ScopePath{
			{Path: "/a/b", Action: ActionInclude, Reason: "r1"},
			{Path: "/a/b", Action: ActionInclude, Reason: "r2"},
		}},
		{"Clean 后等价（尾斜杠）", []ScopePath{
			{Path: "/a/b/", Action: ActionInclude, Reason: "r1"},
			{Path: "/a/b", Action: ActionInclude, Reason: "r2"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertNoSideEffect(t, l, func() error {
				_, err := l.SetScope("app", Scope{Include: true, Paths: c.paths}, scT1)
				return err
			})
		})
	}
}

// 11. 不存在的条目 → ActionNotPresent（业务结果，无 error）；
// 空 name → error。
func TestSetScopeNotPresentAndEmptyName(t *testing.T) {
	l := newList()
	seedEntry(t, l, "neovim")

	act, err := l.SetScope("ghost", nvScope(), scT1)
	if err != nil {
		t.Fatalf("SetScope(ghost) error = %v, want nil (not_present 是业务结果)", err)
	}
	if act != ActionNotPresent {
		t.Errorf("Action = %q, want %q", act, ActionNotPresent)
	}

	assertNoSideEffect(t, l, func() error {
		_, err := l.SetScope("", nvScope(), scT1)
		return err
	})
}

// 12. 软删条目 → error（先恢复条目再谈范围），零副作用。
func TestSetScopeSoftRemovedEntry(t *testing.T) {
	l := newList()
	seedEntry(t, l, "ghost")
	l.MarkRemoved("ghost", scT1)

	assertNoSideEffect(t, l, func() error {
		_, err := l.SetScope("ghost", nvScope(), scT2)
		return err
	})
}

// 13. SetScope 前后 checked_at / recorded_at 不变——scope 是对话存档，
// 不是与系统核对。
func TestSetScopeKeepsTimestamps(t *testing.T) {
	l := newList()
	p := seedEntry(t, l, "neovim")

	_, err := l.SetScope("neovim", nvScope(), scT1)
	if err != nil {
		t.Fatalf("SetScope() error = %v", err)
	}
	if !p.RecordedAt.Equal(scT0) {
		t.Errorf("RecordedAt = %v, want untouched %v", p.RecordedAt, scT0)
	}
	if !p.CheckedAt.Equal(scT0) {
		t.Errorf("CheckedAt = %v, want untouched %v", p.CheckedAt, scT0)
	}
}

// 14. Upsert（unchanged / updated）与 remove→restore 全程 Config 存活。
func TestUpsertAndRestoreKeepConfig(t *testing.T) {
	l := newList()
	p := seedEntry(t, l, "neovim")
	if _, err := l.SetScope("neovim", nvScope(), scT1); err != nil {
		t.Fatalf("SetScope() error = %v", err)
	}

	// unchanged：一个字段都不动，Config 更不动。
	act, _, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "1.0", Source: "pacman"}, scT2)
	if err != nil || act != ActionUnchanged {
		t.Fatalf("Upsert() = %q, %v, want unchanged", act, err)
	}
	if p.Config == nil || p.Config.Reason != nvScope().Reason {
		t.Errorf("Config after unchanged = %+v, want alive", p.Config)
	}

	// updated：三字段更新，Config 保留。
	act, _, err = l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "2.0", Source: "pacman"}, scT2)
	if err != nil || act != ActionUpdated {
		t.Fatalf("Upsert() = %q, %v, want updated", act, err)
	}
	if p.Config == nil || len(p.Config.Paths) != 2 {
		t.Errorf("Config after updated = %+v, want alive", p.Config)
	}

	// remove → restore：软删不清存档，restore 后原样保留。
	l.MarkRemoved("neovim", scT3)
	if p.Config == nil {
		t.Error("Config cleared by soft removal, want kept")
	}
	act, _, err = l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "2.0", Source: "pacman"}, scT3)
	if err != nil || act != ActionRestored {
		t.Fatalf("Upsert() = %q, %v, want restored", act, err)
	}
	if p.Config == nil || p.Config.Reason != nvScope().Reason || len(p.Config.Paths) != 2 {
		t.Errorf("Config after restore = %+v, want original judgment (判断不因删了又加而失忆)", p.Config)
	}
}

// 15. include:false 却带 paths → error：不备份却给路径清单，与
// “include:true 无 include 路径”对称的自相矛盾（卡片未列，本实现
// 自主补上的规则，已向设计会话报备；DESIGN 已补进校验清单）。
func TestSetScopeNoBackupWithPathsRejected(t *testing.T) {
	l := newList()
	seedEntry(t, l, "htop")

	assertNoSideEffect(t, l, func() error {
		_, err := l.SetScope("htop", Scope{
			Include: false,
			Reason:  "not backing up",
			Paths:   []ScopePath{{Path: "~/.config/htop", Action: ActionInclude, Reason: "r"}},
		}, scT1)
		return err
	})
}

// 16. Load 不做 scope 业务校验：结构上违法的 scope（洞在墙外），
// 格式合法即接受——结构校验是 SetScope 的职责，备份执行前再验。
func TestLoadSkipsScopeValidation(t *testing.T) {
	dir := t.TempDir()
	fixture := `{
  "version": 1,
  "revision": 3,
  "updated_at": "2026-11-24T10:00:00Z",
  "packages": {
    "app": {
      "name": "app",
      "source": "pacman",
      "version": "1.0",
      "type": "other",
      "recorded_at": "2026-11-24T10:00:00Z",
      "checked_at": "2026-11-24T10:00:00Z",
      "config": {
        "include": true,
        "paths": [
          { "path": "/etc/app",  "action": "include", "reason": "wall" },
          { "path": "~/.zshrc",  "action": "exclude", "reason": "hole outside any wall (hand-edited)" }
        ]
      }
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "list.json"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	l, err := Open(dir).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v (读路径不做业务校验，手改文件是用户的自救通道)", err)
	}
	p := l.Packages["app"]
	if p.Config == nil || len(p.Config.Paths) != 2 {
		t.Fatalf("Config = %+v, want loaded as-is", p.Config)
	}
}

package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

// path_inspect 用例用 guardExec（它不碰命令）+ 注入 home（探查对象可控）；
// scope_set 用例同样 guardExec——它只写清单。

// 2. path_inspect 真临时 home → ok、facts 逐字段。
func TestPathInspectFacts(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cs, _, _ := newSessionWithHome(t, guardExec{}, home)

	res, out := callTool[pathInspectOut](t, cs, "pdp_path_inspect",
		map[string]any{"paths": []string{"~/notes.txt"}})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if len(out.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(out.Entries))
	}
	f := out.Entries[0]
	if f.Path != "~/notes.txt" || !f.Exists || f.Kind != pathobserver.KindFile || f.Size != 5 {
		t.Errorf("Facts = %+v", f)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("Warnings = %v, want empty", out.Warnings)
	}
}

// 3. path_inspect floor 命中 → 信封 warnings 非空。
func TestPathInspectFloorWarning(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ssh, "id_rsa"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cs, _, _ := newSessionWithHome(t, guardExec{}, home)

	res, out := callTool[pathInspectOut](t, cs, "pdp_path_inspect",
		map[string]any{"paths": []string{"~/.ssh"}})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	f := out.Entries[0]
	if len(f.SensitiveEntries) != 1 || f.SensitiveEntries[0] != "id_rsa" {
		t.Fatalf("SensitiveEntries = %v, want [id_rsa]", f.SensitiveEntries)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("Warnings = empty, want 敏感底线命中必须进信封 warnings")
	}
	if !strings.Contains(out.Warnings[0], "id_rsa") {
		t.Errorf("warning %q 没有点名私钥", out.Warnings[0])
	}
}

// 4. path_inspect 相对路径 → rejected（业务结果，不是 IsError）。
func TestPathInspectRejected(t *testing.T) {
	cs, _, _ := newSession(t)

	res, out := callTool[pathInspectOut](t, cs, "pdp_path_inspect",
		map[string]any{"paths": []string{"foo/bar"}})
	if res.IsError {
		t.Fatalf("IsError = true：rejected 是业务结果")
	}
	if out.Status != StatusRejected {
		t.Errorf("Status = %q, want %q", out.Status, StatusRejected)
	}
	if out.Summary == "" {
		t.Error("Summary = empty, want rejection reason")
	}
}

// 5. scope_set 成功 → ok + action set + 回显 decided_at 非零。
func TestScopeSetSuccess(t *testing.T) {
	cs, rec, _ := newSession(t)
	seed(t, rec, "neovim", recorder.TypeDevTool) // revision 1

	res, out := callTool[scopeSetOut](t, cs, "pdp_scope_set", map[string]any{
		"name": "neovim",
		"scope": map[string]any{
			"include": true,
			"reason":  "user confirmed config is worth keeping",
			"paths": []map[string]any{
				{"path": "~/.config/nvim", "action": "include", "reason": "main config"},
			},
		},
		"base_revision": 1,
	})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if out.Action != recorder.ActionSet {
		t.Errorf("Action = %q, want %q", out.Action, recorder.ActionSet)
	}
	if out.Revision != 2 {
		t.Errorf("Revision = %d, want 2", out.Revision)
	}
	if out.Scope.DecidedAt.IsZero() {
		t.Error("回显 Scope.DecidedAt = zero, want 锁内写入的时间戳")
	}
	if !out.Scope.Include || len(out.Scope.Paths) != 1 {
		t.Errorf("回显 Scope = %+v", out.Scope)
	}
}

// 6. scope_set 过期 revision → conflict + revision 是盘上当前值。
func TestScopeSetConflict(t *testing.T) {
	cs, rec, _ := newSession(t)
	seed(t, rec, "neovim", recorder.TypeDevTool) // revision 1

	res, out := callTool[scopeSetOut](t, cs, "pdp_scope_set", map[string]any{
		"name": "neovim",
		"scope": map[string]any{
			"include": false,
			"reason":  "not backing up",
		},
		"base_revision": 0, // 过期
	})
	if res.IsError {
		t.Fatalf("IsError = true：conflict 是业务结果")
	}
	if out.Status != StatusConflict {
		t.Errorf("Status = %q, want %q", out.Status, StatusConflict)
	}
	if out.Revision != 1 {
		t.Errorf("Revision = %d, want 盘上当前值 1（LLM 直接当新 base_revision 用）", out.Revision)
	}
}

// 7. scope_set 缺 path reason → rejected（ErrInvalidScope 的信封归宿）。
func TestScopeSetRejected(t *testing.T) {
	cs, rec, _ := newSession(t)
	seed(t, rec, "neovim", recorder.TypeDevTool)

	res, out := callTool[scopeSetOut](t, cs, "pdp_scope_set", map[string]any{
		"name": "neovim",
		"scope": map[string]any{
			"include": true,
			"paths": []map[string]any{
				{"path": "~/.config/nvim", "action": "include", "reason": ""},
			},
		},
		"base_revision": 1,
	})
	if res.IsError {
		t.Fatalf("IsError = true：rejected 是业务结果")
	}
	if out.Status != StatusRejected {
		t.Errorf("Status = %q, want %q", out.Status, StatusRejected)
	}
	if out.Summary == "" {
		t.Error("Summary = empty, want 拒绝原因（缺 reason 是可审计的拒绝）")
	}
}

// 8. scope_set 不存在的包 → not_found + action not_present。
func TestScopeSetNotFound(t *testing.T) {
	cs, _, _ := newSession(t)

	res, out := callTool[scopeSetOut](t, cs, "pdp_scope_set", map[string]any{
		"name": "ghost",
		"scope": map[string]any{
			"include": false,
			"reason":  "not backing up",
		},
		"base_revision": 0,
	})
	if res.IsError {
		t.Fatalf("IsError = true：not_present 是业务结果")
	}
	if out.Status != StatusNotFound {
		t.Errorf("Status = %q, want %q", out.Status, StatusNotFound)
	}
	if out.Action != recorder.ActionNotPresent {
		t.Errorf("Action = %q, want %q", out.Action, recorder.ActionNotPresent)
	}
}

// 9. 端到端：add → scope_set → list_show，条目带 config（含 decided_at）。
// 阶段二判断链的协议层全链：探查证据 → 存档 → 呈现。
func TestScopeSetThenShowEndToEnd(t *testing.T) {
	st := pacmanStub("ghostty 1.2.3\n", "", nil, "")
	cs, rec, _ := newSessionWith(t, st)

	// 全程走协议，不经任何 seed 后门。
	_, addOut := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "ghostty", "type": "devtool", "base_revision": 0})
	if addOut.Action != "added" || addOut.Revision != 1 {
		t.Fatalf("add = %q/%d, want added/1", addOut.Action, addOut.Revision)
	}

	res2, setOut := callTool[scopeSetOut](t, cs, "pdp_scope_set", map[string]any{
		"name": "ghostty",
		"scope": map[string]any{
			"include": true,
			"reason":  "user confirmed ghostty config",
			"paths": []map[string]any{
				{"path": "~/.config/ghostty", "action": "include", "reason": "main config dir"},
				{"path": "~/.config/ghostty/secrets", "action": "exclude", "reason": "tokens, user chose to drop"},
			},
		},
		"base_revision": 1,
	})
	if res2.IsError || setOut.Status != StatusOK || setOut.Action != recorder.ActionSet {
		t.Fatalf("scope_set = %q/%q, want ok/set", setOut.Status, setOut.Action)
	}

	res3, showOut := callTool[listShowOut](t, cs, "pdp_list_show", map[string]any{})
	if res3.IsError {
		t.Fatalf("IsError = true: %v", res3.Content)
	}
	if len(showOut.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(showOut.Entries))
	}
	e := showOut.Entries[0]
	if e.Name != "ghostty" {
		t.Fatalf("Entries[0].Name = %q, want ghostty", e.Name)
	}
	if e.Config == nil {
		t.Fatal("Config = nil, want 判断已存档")
	}
	if !e.Config.Include || len(e.Config.Paths) != 2 {
		t.Errorf("Config = %+v", e.Config)
	}
	if e.Config.Paths[1].Action != recorder.ActionExclude {
		t.Errorf("Paths[1].Action = %q, want exclude（洞在墙里的端到端验证）", e.Config.Paths[1].Action)
	}
	if e.Config.DecidedAt.IsZero() {
		t.Error("Config.DecidedAt = zero, want 落盘的时间戳")
	}

	// 盘上核对（真 recorder）。
	l, err := rec.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg := l.Packages["ghostty"].Config; cfg == nil || len(cfg.Paths) != 2 {
		t.Errorf("盘上 Config = %+v, want 两路径的存档", cfg)
	}
}

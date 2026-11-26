package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/executor"
	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
	"github.com/g726unknown/pdp/internal/snapper"
)

// guardExec 不是第三份 stub，是负向断言：纯读工具不该发起任何探查，
// 谁敢调命令就让工具调用当场失败——比"断言 stub 调用 0 次"更硬。
type guardExec struct{}

func (guardExec) Run(context.Context, string, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("unexpected command call")
}

// stubExec 是 mcpserver 自持的函数式假命令（照 executor 测试的样式），
// check、add 等要探查的工具用 newSessionWith 注入。
type stubExec struct {
	calls []string
	run   func(name string, args []string) ([]byte, []byte, error)
}

func (s *stubExec) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	return s.run(name, args)
}

// pacmanStub 按给定的 -Q / -Qm 输出应答，--version 给可用横幅。
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
		case "--version":
			return []byte("Pacman v6.1.0 - libalpm v15.0.0\n"), nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected args %v", args)
		}
	}}
}

// sysStub 应答 uname 与 pacman --version，覆盖 system_info 的探查面。
func sysStub(kernel, arch string, pacmanAvail bool) *stubExec {
	return &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		switch name {
		case "uname":
			switch args[0] {
			case "-r":
				return []byte(kernel + "\n"), nil, nil
			case "-m":
				return []byte(arch + "\n"), nil, nil
			}
		case "pacman":
			if !pacmanAvail {
				return nil, nil, fmt.Errorf("exec pacman: %w", exec.ErrNotFound)
			}
			return []byte("Pacman v6.1.0 - libalpm v15.0.0\n"), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected %s %v", name, args)
	}}
}

var showSeedTime = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

// seed 用真 recorder 种清单数据。
func seed(t *testing.T, rec *recorder.Recorder, name, typ string) {
	t.Helper()
	seedVersion(t, rec, name, "1.0", typ, executor.SourcePacman)
}

func seedVersion(t *testing.T, rec *recorder.Recorder, name, version, typ, source string) {
	t.Helper()
	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = rec.Update(context.Background(), l.Revision, func(li *recorder.List) error {
		_, _, uerr := li.Upsert(name,
			recorder.Facts{Type: typ, Version: version, Source: source}, showSeedTime)
		return uerr
	})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// newSessionWithHome 是 newSessionWith 的 home 注入版：
// path_inspect 用例要在探查对象的 home 里建真实文件。
func newSessionWithHome(t *testing.T, exec observer.Execer, home string) (*mcp.ClientSession, *recorder.Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	rec := recorder.Open(dir)
	svc := executor.New(observer.New(exec), rec, pathobserver.New(home), snapper.New(t.TempDir(), home), home)

	srv := mcp.NewServer(&mcp.Implementation{Name: "pdp-test", Version: "0"}, nil)
	Register(srv, svc, Config{StateDir: dir})

	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, rec, dir
}

// newSessionWith 起一台挂好全部工具的 in-memory MCP server 并接上客户端；
// 命令执行由各测试注入（guardExec 或应答脚本）。
func newSessionWith(t *testing.T, exec observer.Execer) (*mcp.ClientSession, *recorder.Recorder, string) {
	t.Helper()
	return newSessionWithHome(t, exec, t.TempDir())
}

// newSession 用 guardExec 起会话，供纯读工具的用例。
func newSession(t *testing.T) (*mcp.ClientSession, *recorder.Recorder, string) {
	t.Helper()
	return newSessionWith(t, guardExec{})
}

// callTool 走完整协议路径调用工具，把 structuredContent 序列化
// 再反序列化回 Out——从协议层断言，而不是直接调 handler。
func callTool[Out any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, Out) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v (protocol-level failure)", name, err)
	}
	var out Out
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal StructuredContent: %v", err)
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal %s output: %v", name, err)
		}
	}
	return res, out
}

func TestListToolsOrder(t *testing.T) {
	cs, _, _ := newSession(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	// SDK 的 featureSet 按名字母序输出（源码定论：注册顺序不进
	// tools/list）。字母序同样是确定性顺序——客户端缓存的依据
	// 是“每次列出相同”，不是“按注册序”。
	want := []string{
		"pdp_backup_sync",
		"pdp_list_add", "pdp_list_check", "pdp_list_remove", "pdp_list_show",
		"pdp_package_query", "pdp_path_inspect", "pdp_scope_set", "pdp_system_info",
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("len(Tools) = %d, want %d", len(res.Tools), len(want))
	}
	for i, name := range want {
		if res.Tools[i].Name != name {
			t.Errorf("Tools[%d].Name = %q, want %q (字母序，确定性顺序)", i, res.Tools[i].Name, name)
		}
	}

	// 关键 annotations：读工具 ReadOnlyHint，写工具 IdempotentHint。
	// pdp_scope_set 两边都不占：重调刷新 decided_at，是覆盖写不是幂等。
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{
		"pdp_system_info", "pdp_package_query", "pdp_path_inspect",
		"pdp_list_check", "pdp_list_show",
	} {
		if a := byName[name].Annotations; a == nil || !a.ReadOnlyHint {
			t.Errorf("%s Annotations.ReadOnlyHint = false/nil, want true", name)
		}
	}
	for _, name := range []string{"pdp_list_add", "pdp_list_remove"} {
		if a := byName[name].Annotations; a == nil || !a.IdempotentHint {
			t.Errorf("%s Annotations.IdempotentHint = false/nil, want true", name)
		}
	}
}

func TestEnvelopeFlattenedOnAllTools(t *testing.T) {
	cs, _, _ := newSession(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(res.Tools) != 9 {
		t.Fatalf("len(Tools) = %d, want 9", len(res.Tools))
	}
	for _, tool := range res.Tools {
		if tool.Description == "" {
			t.Errorf("%s Description = empty, want non-empty (LLM 依据它决定何时调用)", tool.Name)
		}
		schema, ok := tool.OutputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s OutputSchema 类型 = %T, want map[string]any", tool.Name, tool.OutputSchema)
		}
		if _, ok := schema["allOf"]; ok {
			t.Errorf("%s output schema 含 allOf, want 平铺", tool.Name)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema[properties] 类型 = %T", tool.Name, schema["properties"])
		}
		for _, key := range []string{"status", "summary"} {
			if _, ok := props[key]; !ok {
				t.Errorf("%s output schema 缺少平铺字段 %q", tool.Name, key)
			}
		}
		if _, ok := props["envelope"]; ok {
			t.Errorf("%s output schema 出现未平铺的 envelope 键", tool.Name)
		}
	}
}

func TestSystemInfo(t *testing.T) {
	st := sysStub("6.12.1-arch1-1", "x86_64", true)
	cs, _, dir := newSessionWith(t, st)

	res, out := callTool[systemInfoOut](t, cs, "pdp_system_info", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	// OS 的 id/name/version 读真机 /etc/os-release，只断言 id 与非空。
	if out.OS.ID != "arch" {
		t.Errorf("OS.ID = %q, want arch (真机 os-release)", out.OS.ID)
	}
	if out.OS.Name == "" {
		t.Error("OS.Name = empty, want non-empty")
	}
	if out.OS.Kernel != "6.12.1-arch1-1" || out.OS.Arch != "x86_64" {
		t.Errorf("OS.Kernel/Arch = %q/%q, want 6.12.1-arch1-1/x86_64", out.OS.Kernel, out.OS.Arch)
	}
	if !out.PackageManager.Available || out.PackageManager.Version == "" {
		t.Errorf("PackageManager = %+v, want available with version", out.PackageManager)
	}
	if out.User.Name == "" || out.User.Home == "" {
		t.Errorf("User = %+v, want non-empty", out.User)
	}
	if out.Paths.StateDir != dir {
		t.Errorf("Paths.StateDir = %q, want cfg 注入的 %q", out.Paths.StateDir, dir)
	}
	if len(out.Capabilities) != 1 || out.Capabilities[0] != "pacman" {
		t.Errorf("Capabilities = %v, want [pacman]", out.Capabilities)
	}
	if out.Summary == "" {
		t.Error("Summary = empty, want non-empty")
	}
}

func TestSystemInfoPacmanMissing(t *testing.T) {
	st := sysStub("6.12.1-arch1-1", "x86_64", false)
	cs, _, _ := newSessionWith(t, st)

	res, out := callTool[systemInfoOut](t, cs, "pdp_system_info", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true：没有 pacman 的机器上 system_info 仍要能回答")
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q（不可用不是错误）", out.Status, StatusOK)
	}
	if out.PackageManager.Available {
		t.Error("PackageManager.Available = true, want false")
	}
	if out.PackageManager.Version != "" {
		t.Errorf("PackageManager.Version = %q, want empty", out.PackageManager.Version)
	}
	if out.Capabilities == nil || len(out.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want non-nil empty", out.Capabilities)
	}
}

func TestPackageQuery(t *testing.T) {
	st := pacmanStub("neovim 0.11.2\n", "", nil, "")
	cs, _, _ := newSessionWith(t, st)

	res, out := callTool[packageQueryOut](t, cs, "pdp_package_query",
		map[string]any{"names": []string{"neovim", "htop"}})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 (entries 覆盖全部请求名)", len(out.Entries))
	}
	if out.Entries[0].Name != "neovim" || !out.Entries[0].Installed ||
		out.Entries[0].Version != "0.11.2" || out.Entries[0].Source != executor.SourcePacman {
		t.Errorf("neovim = %+v", out.Entries[0])
	}
	if out.Entries[1].Name != "htop" || out.Entries[1].Installed ||
		out.Entries[1].Version != "" || out.Entries[1].Source != executor.SourceUnknown {
		t.Errorf("htop = %+v", out.Entries[1])
	}
	if len(out.Missing) != 1 || out.Missing[0] != "htop" {
		t.Errorf("Missing = %v, want [htop]", out.Missing)
	}

	// 名单含空串 → rejected（业务结果，不是 IsError）。
	res2, out2 := callTool[packageQueryOut](t, cs, "pdp_package_query",
		map[string]any{"names": []string{"neovim", ""}})
	if res2.IsError {
		t.Fatalf("IsError = true：rejected 是业务结果")
	}
	if out2.Status != StatusRejected {
		t.Errorf("Status = %q, want %q", out2.Status, StatusRejected)
	}
	if out2.Entries == nil || len(out2.Entries) != 0 {
		t.Errorf("Entries = %v, want non-nil empty", out2.Entries)
	}
}

func TestListAddAddedThenUnchanged(t *testing.T) {
	st := pacmanStub("neovim 0.11.2\n", "", nil, "")
	cs, rec, _ := newSessionWith(t, st)

	res, out := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "neovim", "type": "devtool", "base_revision": 0})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK || out.Action != "added" || out.Revision != 1 {
		t.Errorf("Status/Action/Revision = %q/%q/%d, want ok/added/1", out.Status, out.Action, out.Revision)
	}
	if out.Package.Name != "neovim" || out.Package.Version != "0.11.2" ||
		out.Package.Source != executor.SourcePacman || out.Package.Type != "devtool" {
		t.Errorf("Package = %+v", out.Package)
	}

	res2, out2 := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "neovim", "type": "devtool", "base_revision": 1})
	if res2.IsError {
		t.Fatalf("IsError = true: %v", res2.Content)
	}
	if out2.Status != StatusOK || out2.Action != "unchanged" || out2.Revision != 2 {
		t.Errorf("Status/Action/Revision = %q/%q/%d, want ok/unchanged/2", out2.Status, out2.Action, out2.Revision)
	}

	// 协议层验证落盘。
	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if l.Revision != 2 {
		t.Errorf("on-disk Revision = %d, want 2", l.Revision)
	}
}

func TestListAddRejected(t *testing.T) {
	st := pacmanStub("", "", nil, "")
	cs, _, _ := newSessionWith(t, st)

	res, out := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "neovim", "type": "garbage", "base_revision": 0})
	if res.IsError {
		t.Fatalf("IsError = true：rejected 是业务结果")
	}
	if out.Status != StatusRejected {
		t.Errorf("Status = %q, want %q (非法 type)", out.Status, StatusRejected)
	}
	if out.Summary == "" {
		t.Error("Summary = empty, want rejection reason")
	}

	res2, out2 := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "", "type": "devtool", "base_revision": 0})
	if res2.IsError {
		t.Fatalf("IsError = true：rejected 是业务结果")
	}
	if out2.Status != StatusRejected {
		t.Errorf("Status = %q, want %q (空名)", out2.Status, StatusRejected)
	}

	// 拒绝的调用不花一次子进程（executor 的保证在协议层的兑现）。
	if len(st.calls) != 0 {
		t.Errorf("exec calls = %d, want 0: %v", len(st.calls), st.calls)
	}
}

func TestListAddConflict(t *testing.T) {
	st := pacmanStub("neovim 0.11.2\n", "", nil, "")
	cs, rec, _ := newSessionWith(t, st)

	// 先用真 recorder 把 revision 顶到 1。
	seed(t, rec, "htop", recorder.TypeOther)

	res, out := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "neovim", "type": "devtool", "base_revision": 0})
	if res.IsError {
		t.Fatalf("IsError = true：conflict 是业务结果")
	}
	if out.Status != StatusConflict {
		t.Errorf("Status = %q, want %q", out.Status, StatusConflict)
	}
	if out.Revision != 1 {
		t.Errorf("Revision = %d, want 盘上当前值 1 (LLM 直接当新 base_revision 用)", out.Revision)
	}
	if out.Summary == "" {
		t.Error("Summary = empty, want retry guidance")
	}
}

func TestListAddUninstalledWarning(t *testing.T) {
	st := pacmanStub("", "error: package 'htop' was not found\n", errors.New("exit status 1"), "")
	cs, _, _ := newSessionWith(t, st)

	res, out := callTool[listAddOut](t, cs, "pdp_list_add",
		map[string]any{"name": "htop", "type": "other", "base_revision": 0})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK || out.Action != "added" {
		t.Errorf("Status/Action = %q/%q, want ok/added (未安装仍然记录)", out.Status, out.Action)
	}
	if out.Package.Version != "" || out.Package.Source != executor.SourceUnknown {
		t.Errorf("Package.Version/Source = %q/%q, want empty/unknown", out.Package.Version, out.Package.Source)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("len(Warnings) = %d, want 1", len(out.Warnings))
	}
}

func TestListRemoveBranches(t *testing.T) {
	cs, rec, _ := newSession(t) // guardExec：remove 不探查
	seed(t, rec, "neovim", recorder.TypeDevTool)

	res, out := callTool[listRemoveOut](t, cs, "pdp_list_remove",
		map[string]any{"name": "neovim", "base_revision": 1})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK || out.Action != "removed" || out.Revision != 2 {
		t.Errorf("Status/Action/Revision = %q/%q/%d, want ok/removed/2", out.Status, out.Action, out.Revision)
	}

	res2, out2 := callTool[listRemoveOut](t, cs, "pdp_list_remove",
		map[string]any{"name": "neovim", "base_revision": 2})
	if res2.IsError {
		t.Fatalf("IsError = true: %v", res2.Content)
	}
	if out2.Status != StatusOK || out2.Action != "already_removed" {
		t.Errorf("Status/Action = %q/%q, want ok/already_removed", out2.Status, out2.Action)
	}

	// 删不存在的：信封 not_found 的唯一来源（executor 返回正常结果）。
	res3, out3 := callTool[listRemoveOut](t, cs, "pdp_list_remove",
		map[string]any{"name": "ghost", "base_revision": 3})
	if res3.IsError {
		t.Fatalf("IsError = true: not_present 不是工具错误")
	}
	if out3.Status != StatusNotFound || out3.Action != "not_present" {
		t.Errorf("Status/Action = %q/%q, want not_found/not_present", out3.Status, out3.Action)
	}
	if out3.Revision != 4 {
		t.Errorf("Revision = %d, want 4 (recorder 语义：照常落盘)", out3.Revision)
	}
}

func TestListCheckDrift(t *testing.T) {
	st := pacmanStub("neovim 0.11.2\nhtop 3.5.0\n", "", nil, "")
	cs, rec, _ := newSessionWith(t, st)

	// 种出漂移：neovim 记录与实际一致；htop 记录 3.4.0、实际 3.5.0。
	seedVersion(t, rec, "neovim", "0.11.2", recorder.TypeDevTool, executor.SourcePacman)
	seedVersion(t, rec, "htop", "3.4.0", recorder.TypeOther, executor.SourcePacman)

	res, out := callTool[listCheckOut](t, cs, "pdp_list_check", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if out.Revision != 2 {
		t.Errorf("Revision = %d, want 2", out.Revision)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(out.Entries))
	}
	// 升序：htop, neovim。
	if out.Entries[0].Name != "htop" || out.Entries[1].Name != "neovim" {
		t.Errorf("Entries 顺序 = [%s, %s], want [htop, neovim]", out.Entries[0].Name, out.Entries[1].Name)
	}
	h := out.Entries[0]
	if h.RecordedVersion != "3.4.0" || h.Installed != true || h.ActualVersion != "3.5.0" ||
		h.RecordedSource != executor.SourcePacman || h.ActualSource != executor.SourcePacman {
		t.Errorf("htop = %+v (漂移事实：记录 3.4.0 / 实际 3.5.0)", h)
	}
	n := out.Entries[1]
	if n.RecordedVersion != "0.11.2" || !n.Installed || n.ActualVersion != "0.11.2" {
		t.Errorf("neovim = %+v (一致事实)", n)
	}
	if out.Summary == "" {
		t.Error("Summary = empty, want non-empty")
	}
}

func TestListCheckProbeError(t *testing.T) {
	st := &stubExec{run: func(name string, _ []string) ([]byte, []byte, error) {
		return nil, []byte("error: database 'sync' is inconsistent\n"), errors.New("exit status 1")
	}}
	cs, rec, _ := newSessionWith(t, st)
	seed(t, rec, "neovim", recorder.TypeDevTool)

	res, _ := callTool[listCheckOut](t, cs, "pdp_list_check", map[string]any{})
	if !res.IsError {
		t.Fatal("IsError = false, want true (探查错误是真实故障)")
	}
	if res.StructuredContent != nil {
		t.Errorf("StructuredContent = %v, want nil on tool error", res.StructuredContent)
	}
}

// ---- pdp_list_show（卡 #8 的用例，换用 callTool 泛型）----

func TestShowEmptyList(t *testing.T) {
	cs, _, _ := newSession(t)

	res, out := callTool[listShowOut](t, cs, "pdp_list_show", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if out.Summary != "inventory: 0 active, 0 removed" {
		t.Errorf("Summary = %q, want 新措辞（数字前移绕开单复数）", out.Summary)
	}
	if out.Revision != 0 || out.Total != 0 || out.RemovedCount != 0 {
		t.Errorf("Revision=%d Total=%d RemovedCount=%d, want 0/0/0", out.Revision, out.Total, out.RemovedCount)
	}
	if out.ByType == nil {
		t.Fatal("ByType = nil, want non-nil empty map")
	}
	if len(out.ByType) != 0 {
		t.Errorf("len(ByType) = %d, want 0", len(out.ByType))
	}
	if out.Entries == nil {
		t.Fatal("Entries = nil, want non-nil empty slice")
	}
	if len(out.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(out.Entries))
	}
}

func TestShowSeededEntries(t *testing.T) {
	cs, rec, _ := newSession(t)

	// 故意乱序种入，验证输出按名字升序。
	seed(t, rec, "vim", recorder.TypeDevTool)
	seed(t, rec, "go", recorder.TypeRuntime)
	seed(t, rec, "neovim", recorder.TypeDevTool)

	res, out := callTool[listShowOut](t, cs, "pdp_list_show", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if out.Total != 3 || out.Revision != 3 || out.RemovedCount != 0 {
		t.Errorf("Total=%d Revision=%d RemovedCount=%d, want 3/3/0", out.Total, out.Revision, out.RemovedCount)
	}

	wantOrder := []string{"go", "neovim", "vim"}
	if len(out.Entries) != len(wantOrder) {
		t.Fatalf("len(Entries) = %d, want %d", len(out.Entries), len(wantOrder))
	}
	for i, want := range wantOrder {
		if out.Entries[i].Name != want {
			t.Errorf("Entries[%d].Name = %q, want %q (ascending)", i, out.Entries[i].Name, want)
		}
	}

	nv := out.Entries[1] // neovim
	if nv.Source != executor.SourcePacman || nv.Version != "1.0" || nv.Type != recorder.TypeDevTool {
		t.Errorf("neovim = %+v", nv)
	}
	// 时间经 RFC3339 序列化往返后用 .Equal 比对（time.Time 的 ==
	// 连时区指针一起比，是坑）。
	if !nv.RecordedAt.Equal(showSeedTime) {
		t.Errorf("neovim.RecordedAt = %v, want %v", nv.RecordedAt, showSeedTime)
	}
	if !nv.CheckedAt.Equal(showSeedTime) {
		t.Errorf("neovim.CheckedAt = %v, want %v", nv.CheckedAt, showSeedTime)
	}

	if out.ByType[recorder.TypeDevTool] != 2 || out.ByType[recorder.TypeRuntime] != 1 {
		t.Errorf("ByType = %v, want devtool:2 runtime:1", out.ByType)
	}
	if out.Summary != "inventory: 3 active, 0 removed" {
		t.Errorf("Summary = %q, want 新措辞", out.Summary)
	}
}

func TestShowSkipsRemovedEntry(t *testing.T) {
	cs, rec, _ := newSession(t)

	seed(t, rec, "neovim", recorder.TypeDevTool)
	seed(t, rec, "ghost", recorder.TypeOther)
	l, err := rec.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(context.Background(), l.Revision, func(li *recorder.List) error {
		li.MarkRemoved("ghost", showSeedTime)
		return nil
	}); err != nil {
		t.Fatalf("remove ghost: %v", err)
	}

	res, out := callTool[listShowOut](t, cs, "pdp_list_show", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Total != 1 || out.RemovedCount != 1 {
		t.Errorf("Total=%d RemovedCount=%d, want 1/1", out.Total, out.RemovedCount)
	}
	if len(out.Entries) != 1 || out.Entries[0].Name != "neovim" {
		t.Errorf("Entries = %+v, want only neovim", out.Entries)
	}
	if _, ok := out.ByType[recorder.TypeOther]; ok {
		t.Errorf("removed entry's type leaked into ByType: %v", out.ByType)
	}
	// 单数场景正是旧措辞（"1 packages recorded"）翻车的地方。
	if out.Summary != "inventory: 1 active, 1 removed" {
		t.Errorf("Summary = %q, want 单数也不翻车的新措辞", out.Summary)
	}
}

func TestShowCorruptFileIsToolError(t *testing.T) {
	cs, _, dir := newSession(t)

	if err := os.WriteFile(filepath.Join(dir, "list.json"), []byte(`{"坏`), 0o644); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pdp_list_show", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool() error = %v (want protocol success, tool-level error)", err)
	}
	if !res.IsError {
		t.Error("IsError = false, want true (corrupt list.json is a real fault)")
	}
	if res.StructuredContent != nil {
		t.Errorf("StructuredContent = %v, want nil on tool error", res.StructuredContent)
	}
}

package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/recorder"
)

// BackupSync 的协议层用例：guardExec（sync 不探查）+ 注入 home
//（探查对象可控）+ 真 snapper（临时 backups root，经 harness 装配）。

// backupSeedScope 经真 recorder 落一个带 scope 的条目，无后门。
func backupSeedScope(t *testing.T, cs *mcp.ClientSession, rec *recorder.Recorder, name string, sc recorder.Scope) {
	t.Helper()
	l, err := rec.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(t.Context(), l.Revision, func(li *recorder.List) error {
		if _, _, uerr := li.Upsert(name,
			recorder.Facts{Type: recorder.TypeDevTool, Version: "1.0", Source: "pacman"}, time.Now()); uerr != nil {
			return uerr
		}
		_, serr := li.SetScope(name, sc, time.Now())
		return serr
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// backupSeedBare 种一个从未讨论过范围的条目（NoScopeYet 素材）。
func backupSeedBare(t *testing.T, rec *recorder.Recorder, name string) {
	t.Helper()
	l, err := rec.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(t.Context(), l.Revision, func(li *recorder.List) error {
		_, _, uerr := li.Upsert(name,
			recorder.Facts{Type: recorder.TypeOther, Version: "1.0", Source: "pacman"}, time.Now())
		return uerr
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// 8. happy path → ok、snapshot_id 非空、floor_included 与 warnings 点名。
func TestBackupSyncHappyPath(t *testing.T) {
	home := t.TempDir()
	wall := filepath.Join(home, ".config", "x")
	if err := os.MkdirAll(wall, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wall, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	pem := filepath.Join(wall, "secret.pem")
	if err := os.WriteFile(pem, []byte("key"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cs, rec, _ := newSessionWithHome(t, guardExec{}, home)
	backupSeedScope(t, cs, rec, "x", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/x", Action: recorder.ActionInclude, Reason: "wall"},
	}})
	backupSeedBare(t, rec, "y") // 无范围 → NoScopeYet

	res, out := callTool[backupSyncOut](t, cs, "pdp_backup_sync", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q（floor_included 与 NoScopeYet 是话不是失败）", out.Status, StatusOK)
	}
	if out.SnapshotID == "" {
		t.Error("SnapshotID = empty, want 非空")
	}
	if len(out.FloorIncluded) != 1 || !strings.HasSuffix(out.FloorIncluded[0], "secret.pem") {
		t.Errorf("FloorIncluded = %v, want 点名 secret.pem", out.FloorIncluded)
	}
	if len(out.Warnings) == 0 || !strings.Contains(out.Warnings[0], "y") {
		t.Errorf("Warnings = %v, want NoScopeYet 点名 y", out.Warnings)
	}
	if out.Diff.Added != 2 || out.FilesTotal != 2 {
		t.Errorf("Added/FilesTotal = %d/%d, want 2/2", out.Diff.Added, out.FilesTotal)
	}
}

// 9. dry_run → snapshot_id 为空、status ok、零写入。
func TestBackupSyncDryRun(t *testing.T) {
	home := t.TempDir()
	wall := filepath.Join(home, ".config", "x")
	if err := os.MkdirAll(wall, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wall, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cs, rec, _ := newSessionWithHome(t, guardExec{}, home)
	backupSeedScope(t, cs, rec, "x", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/x", Action: recorder.ActionInclude, Reason: "wall"},
	}})

	res, out := callTool[backupSyncOut](t, cs, "pdp_backup_sync",
		map[string]any{"dry_run": true})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusOK {
		t.Errorf("Status = %q, want %q", out.Status, StatusOK)
	}
	if out.SnapshotID != "" {
		t.Errorf("SnapshotID = %q, want 空", out.SnapshotID)
	}
	if out.Diff.Added != 1 {
		t.Errorf("Added = %d, want 1（dry_run 也要算得出 diff）", out.Diff.Added)
	}
}

// 10. skipped_error 非空 → partial（有想做的没做完）。
func TestBackupSyncPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位约束")
	}
	home := t.TempDir()
	wall := filepath.Join(home, ".config", "x")
	if err := os.MkdirAll(wall, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	blocked := filepath.Join(wall, "blocked.txt")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0o644) })
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	cs, rec, _ := newSessionWithHome(t, guardExec{}, home)
	backupSeedScope(t, cs, rec, "x", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/x", Action: recorder.ActionInclude, Reason: "wall"},
	}})

	res, out := callTool[backupSyncOut](t, cs, "pdp_backup_sync", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}
	if out.Status != StatusPartial {
		t.Errorf("Status = %q, want %q（skipped_error 非空 = 有想做的没做完）", out.Status, StatusPartial)
	}
	if len(out.SkippedError) != 1 || !strings.Contains(out.SkippedError[0], "blocked.txt") {
		t.Errorf("SkippedError = %v, want 点名 blocked.txt", out.SkippedError)
	}
}

// 11. 资源：sync 后读 manifest → JSON 齐全；无快照 → 资源不存在。
func TestBackupManifestResource(t *testing.T) {
	home := t.TempDir()
	wall := filepath.Join(home, ".config", "x")
	if err := os.MkdirAll(wall, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wall, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cs, rec, _ := newSessionWithHome(t, guardExec{}, home)

	// 无快照：首次 sync 前的合法状态 = 资源不存在（不是错误）。
	if _, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://backup/manifest"}); err == nil {
		t.Error("无快照时 ReadResource error = nil, want 资源不存在")
	}

	backupSeedScope(t, cs, rec, "x", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/x", Action: recorder.ActionInclude, Reason: "wall"},
	}})
	res, out := callTool[backupSyncOut](t, cs, "pdp_backup_sync", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true: %v", res.Content)
	}

	rres, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://backup/manifest"})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if len(rres.Contents) != 1 || rres.Contents[0].MIMEType != "application/json" {
		t.Fatalf("contents = %+v", rres.Contents)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(rres.Contents[0].Text), &m); err != nil {
		t.Fatalf("parse manifest JSON: %v", err)
	}
	if m["snapshot_id"] != out.SnapshotID {
		t.Errorf("manifest snapshot_id = %v, want 与 sync 输出一致 %q", m["snapshot_id"], out.SnapshotID)
	}
	files, ok := m["files"].([]any)
	if !ok || len(files) != 1 {
		t.Errorf("manifest files = %v, want 1 条", m["files"])
	}
}

// 12. guide 关键字含 pdp_backup_sync（备份同步节在场）。
func TestReadGuideBackupSync(t *testing.T) {
	cs, _, _ := newSession(t)

	res, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://guide"})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	c := res.Contents[0]
	if !strings.Contains(c.Text, "pdp_backup_sync") {
		t.Error("guide text 缺少 pdp_backup_sync 字样（备份同步节应在场）")
	}
	if strings.Contains(c.Text, "备份执行与恢复尚未实现") {
		t.Error("guide text 仍含过时的“备份执行未实现”措辞——诚实校准已落地")
	}
}

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
	"github.com/g726unknown/pdp/internal/snapper"
)

// BackupSync 用例的替身全家福：guardExec（不探查的负向断言）+
// 真 recorder 临时目录 + 真 snapper（临时 home + 临时 backups root）。
// 想要 scope 先经 rec.Update 落档——判断与执行之间没有后门。

func newSyncService(t *testing.T) (*Service, *recorder.Recorder, string, string) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	rec := recorder.Open(t.TempDir())
	svc := New(observer.New(guardExec{}), rec, pathobserver.New(home), snapper.New(root, home), home)
	return svc, rec, home, root
}

// syncSeedScope 种一个带 scope 的条目（真 Load + 真 Update，无后门）。
func syncSeedScope(t *testing.T, rec *recorder.Recorder, name string, sc recorder.Scope) {
	t.Helper()
	ctx := context.Background()
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(ctx, l.Revision, func(li *recorder.List) error {
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

// 1. plan 解析：一墙一洞 → ~ 展开为绝对路径、Explicit 全 true；
// 洞内的文件不进快照。
func TestBackupSyncPlanParsing(t *testing.T) {
	svc, rec, home, root := newSyncService(t)
	syncSeedScope(t, rec, "app", recorder.Scope{
		Include: true,
		Paths: []recorder.ScopePath{
			{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "wall"},
			{Path: "~/.config/direct.txt", Action: recorder.ActionInclude, Reason: "named file"},
			{Path: "~/.config/app/drop", Action: recorder.ActionExclude, Reason: "hole"},
		},
	})
	if err := os.MkdirAll(filepath.Join(home, ".config", "app", "drop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keep := filepath.Join(home, ".config", "app", "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	direct := filepath.Join(home, ".config", "direct.txt")
	if err := os.WriteFile(direct, []byte("d"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "app", "drop", "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := svc.BackupSync(t.Context(), SyncInput{})
	if err != nil {
		t.Fatalf("BackupSync() error = %v", err)
	}
	if res.Snapshot.FilesTotal != 2 {
		t.Fatalf("FilesTotal = %d, want 2（洞不进快照）", res.Snapshot.FilesTotal)
	}

	// ~ 展开为绝对路径；Explicit 分界两侧各钉一条：
	// scope 直接点名的文件 = 显式；墙内扫描到的文件 = 非显式
	//（非显式只来自引擎的目录展开，卡片的原始语义）。
	m, err := svc.snap.Current(t.Context())
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	byPath := map[string]snapper.FileEntry{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	if f := byPath[direct]; !f.Explicit {
		t.Errorf("直接点名的 %s Explicit = false, want true", direct)
	}
	if f := byPath[keep]; f.Explicit {
		t.Errorf("墙内扫描到的 %s Explicit = true, want false", keep)
	}
	if !strings.HasPrefix(m.Files[0].Path, home) {
		t.Errorf("manifest Path = %q, want 展开后的绝对路径（~ 已由 home 解析）", m.Files[0].Path)
	}
	// 快照树镜像布局。
	if _, err := os.Stat(filepath.Join(root, res.Snapshot.SnapshotID, snapperMirror(keep))); err != nil {
		t.Errorf("快照文件缺失: %v", err)
	}
}

// snapperMirror 只为测试免 import 断言路径布局；mirror 是 snapper 的
// 包内函数，等价展开：绝对路径去掉开头的分隔符。
func snapperMirror(abs string) string {
	return strings.TrimPrefix(abs, string(filepath.Separator))
}

// 2. Config==nil → NoScopeYet 点名；include:false → 静默
// （明确决定不打扰——不在 NoScopeYet、不产生 warning）。
func TestBackupSyncNoScopeAndExplicitNo(t *testing.T) {
	svc, rec, home, _ := newSyncService(t)
	ctx := t.Context()

	// never：从未讨论过范围；declined：明确不备份；app：正常墙。
	syncSeedScope(t, rec, "app", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "r"},
	}})
	if err := os.MkdirAll(filepath.Join(home, ".config", "app"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "app", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, name := range []string{"never", "declined"} {
		syncSeedScopeNoScope(t, rec, name)
	}
	syncSeedScope(t, rec, "declined", recorder.Scope{Include: false, Reason: "user declined"})

	res, err := svc.BackupSync(ctx, SyncInput{})
	if err != nil {
		t.Fatalf("BackupSync() error = %v", err)
	}
	if len(res.NoScopeYet) != 1 || res.NoScopeYet[0] != "never" {
		t.Errorf("NoScopeYet = %v, want [never]", res.NoScopeYet)
	}
	// warning 素材：NoScopeYet 进 warnings。
	found := false
	for _, w := range res.Snapshot.Warnings {
		if strings.Contains(w, "never") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want 点名 never", res.Snapshot.Warnings)
	}
	// declined 静默：不出现在任何报告里。
	for _, w := range res.Snapshot.Warnings {
		if strings.Contains(w, "declined") {
			t.Errorf("declined 被打扰了：%q（明确决定应静默）", w)
		}
	}
	if res.Snapshot.FilesTotal != 1 {
		t.Errorf("FilesTotal = %d, want 1", res.Snapshot.FilesTotal)
	}
}

// syncSeedScopeNoScope 种一个从未讨论过范围的条目。
func syncSeedScopeNoScope(t *testing.T, rec *recorder.Recorder, name string) {
	t.Helper()
	ctx := context.Background()
	l, err := rec.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(ctx, l.Revision, func(li *recorder.List) error {
		_, _, uerr := li.Upsert(name,
			recorder.Facts{Type: recorder.TypeOther, Version: "1.0", Source: "pacman"}, time.Now())
		return uerr
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// 3. 真跑：diff.added 与文件数一致、manifest 归属 list_revision。
func TestBackupSyncRealRun(t *testing.T) {
	svc, rec, home, _ := newSyncService(t)
	syncSeedScope(t, rec, "app", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "r"},
	}})
	if err := os.MkdirAll(filepath.Join(home, ".config", "app"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "app", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "app", "b.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	l, err := rec.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	res, err := svc.BackupSync(t.Context(), SyncInput{})
	if err != nil {
		t.Fatalf("BackupSync() error = %v", err)
	}
	if res.Snapshot.Diff.Added != 2 || res.Snapshot.FilesTotal != 2 {
		t.Errorf("Added/FilesTotal = %d/%d, want 2/2", res.Snapshot.Diff.Added, res.Snapshot.FilesTotal)
	}
	m, err := svc.snap.Current(t.Context())
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if m.ListRev != l.Revision {
		t.Errorf("manifest ListRev = %d, want 快照时的清单 revision %d", m.ListRev, l.Revision)
	}
}

// 4. dry_run → 零写入、SnapshotID 空。
func TestBackupSyncDryRun(t *testing.T) {
	svc, rec, home, root := newSyncService(t)
	syncSeedScope(t, rec, "app", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "r"},
	}})
	if err := os.MkdirAll(filepath.Join(home, ".config", "app"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "app", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := svc.BackupSync(t.Context(), SyncInput{DryRun: true})
	if err != nil {
		t.Fatalf("BackupSync() error = %v", err)
	}
	if res.Snapshot.SnapshotID != "" {
		t.Errorf("SnapshotID = %q, want 空", res.Snapshot.SnapshotID)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("backups root 非空: %d 项（dry_run 零写入）", len(entries))
	}
}

// 5. guardExec 全程零命令：sync 读的是存档不是系统——任何探查
// 都会让 guardExec 当场爆炸、BackupSync 失败。本文件所有用例
// 共用 guardExec，这里集中点名这条保证。
func TestBackupSyncNoCommands(t *testing.T) {
	svc, rec, home, _ := newSyncService(t)
	syncSeedScope(t, rec, "app", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "r"},
	}})
	if err := os.MkdirAll(filepath.Join(home, ".config", "app"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := svc.BackupSync(t.Context(), SyncInput{}); err != nil {
		t.Fatalf("BackupSync() error = %v（若因 unexpected command 则是探查泄露）", err)
	}
}

// 7. PDP_HOME 便携性（卡 #17，恢复改道的地基）：
// 完整 PDP_HOME（清单 + 一次真 sync 的快照）用 cp -a 整目录拷到
// "新机"，新 Engine 只认拷贝物——读 manifest 成功、逐文件 sha256
// 全对。"整目录拷贝即可用"从此是测试钉住的事实，不是口头承诺。
func TestPDPHomePortable(t *testing.T) {
	pdpHome := t.TempDir()
	home := t.TempDir()
	rec := recorder.Open(pdpHome)
	svc := New(observer.New(guardExec{}), rec, pathobserver.New(home),
		snapper.New(filepath.Join(pdpHome, "backups"), home), home)

	// 种清单（scope 用 ~ 记录——跨机器的通用语）+ 真跑一次 sync。
	syncSeedScope(t, rec, "app", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "r"},
	}})
	dir := filepath.Join(home, ".config", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("content-a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("content-b"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := svc.BackupSync(t.Context(), SyncInput{})
	if err != nil {
		t.Fatalf("BackupSync() error = %v", err)
	}

	// "新机"：PDP_HOME 整目录 cp -a 过去（guide 恢复节第 1 步的字面操作）。
	newHome := t.TempDir()
	newPDP := t.TempDir()
	if out, err := exec.Command("cp", "-a", pdpHome+string(filepath.Separator)+".", newPDP).CombinedOutput(); err != nil {
		t.Fatalf("cp -a: %v: %s", err, out)
	}

	// 新 Engine（新 PDP_HOME、新 home）只认拷贝物：账本读得懂、
	// 快照文件完好、逐文件哈希全对。
	e2 := snapper.New(filepath.Join(newPDP, "backups"), newHome)
	m, err := e2.Current(t.Context())
	if err != nil {
		t.Fatalf("新机读 manifest: %v", err)
	}
	if m.SnapshotID != res.Snapshot.SnapshotID {
		t.Errorf("manifest snapshot_id = %q, want 原快照 %q", m.SnapshotID, res.Snapshot.SnapshotID)
	}
	if len(m.Files) != 2 {
		t.Fatalf("manifest files = %d, want 2", len(m.Files))
	}
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(newPDP, "backups", m.SnapshotID,
			strings.TrimPrefix(f.Path, string(filepath.Separator))))
		if err != nil {
			t.Fatalf("新机读快照文件 %s: %v", f.Path, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("新机 sha256 不符: %s", f.Path)
		}
	}

	// 新机的 recorder 也读得懂清单（同名用户零调整——~ 语义随 home 走）。
	rec2 := recorder.Open(newPDP)
	l2, err := rec2.Load(t.Context())
	if err != nil {
		t.Fatalf("新机读 list.json: %v", err)
	}
	if l2.Packages["app"] == nil || l2.Packages["app"].Config == nil {
		t.Error("新机清单缺 app 的 scope 存档")
	}
}

// 6. 引擎失败（backups root 只读）→ error 原样上抛，旧账不动。
func TestBackupSyncEngineFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位约束")
	}
	svc, rec, home, root := newSyncService(t)
	syncSeedScope(t, rec, "app", recorder.Scope{Include: true, Paths: []recorder.ScopePath{
		{Path: "~/.config/app", Action: recorder.ActionInclude, Reason: "r"},
	}})
	if err := os.MkdirAll(filepath.Join(home, ".config", "app"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Cleanup(func() { os.Chmod(root, 0o755) })
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	if _, err := svc.BackupSync(t.Context(), SyncInput{}); err == nil {
		t.Fatal("BackupSync() error = nil, want 引擎失败原样上抛（staging 建不起来）")
	}
}

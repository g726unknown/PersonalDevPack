package snapper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本包的"现实"就是临时目录里的假文件树（与 recorder / pathobserver
// 同族：真文件系统当替身，零假实现）。测试直接读文件内容断言——
// 测试不是模型，字节管道限制的是工具结果。
// 第一次碰真文件的时刻在卡 #16 的 e2e。

var (
	syncT0 = time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	syncT1 = time.Date(2026, 11, 24, 15, 0, 5, 0, time.UTC)
	syncT2 = time.Date(2026, 11, 24, 15, 0, 10, 0, time.UTC)
	syncT3 = time.Date(2026, 11, 24, 15, 0, 15, 0, time.UTC)
)

func newTestEngine(t *testing.T) (*Engine, string, string) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	return New(root, home), home, root
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func readManifest(t *testing.T, root, id string) Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, id, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

func snapshotDirCount(t *testing.T, e *Engine) int {
	t.Helper()
	ids, err := e.snapshotIDs()
	if err != nil {
		t.Fatalf("snapshotIDs: %v", err)
	}
	return len(ids)
}

func syncOK(t *testing.T, e *Engine, plan []PkgPlan, now time.Time) Result {
	t.Helper()
	res, err := e.Sync(context.Background(), plan, 7, now, false)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	return res
}

// 1. 首次 sync：文件逐字节一致、manifest 字段齐全、sha256 独立重算核对。
func TestSyncFirstSnapshot(t *testing.T) {
	e, home, root := newTestEngine(t)
	src := writeFile(t, filepath.Join(home, ".config", "app"), "main.conf", "hello world")
	if err := os.Chmod(src, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: src, Explicit: true}}}}

	res := syncOK(t, e, plan, syncT0)
	if res.SnapshotID != "20261124T150000Z" {
		t.Errorf("SnapshotID = %q, want UTC 时间戳", res.SnapshotID)
	}
	if res.FilesTotal != 1 || res.Diff.Added != 1 {
		t.Errorf("FilesTotal=%d Added=%d, want 1/1", res.FilesTotal, res.Diff.Added)
	}

	// 快照内文件逐字节一致；布局 = 绝对路径去掉开头的 /。
	dst := filepath.Join(root, res.SnapshotID, mirror(src))
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("snapshot content = %q, want 原文（测试可以直接读内容——测试不是模型）", got)
	}

	// manifest：sha256 用标准库独立重算核对。
	m := readManifest(t, root, res.SnapshotID)
	if m.SnapshotID != res.SnapshotID || m.ListRev != 7 || len(m.Files) != 1 {
		t.Fatalf("manifest 头部 = %+v, files=%d", m, len(m.Files))
	}
	f := m.Files[0]
	want := sha256.Sum256([]byte("hello world"))
	if f.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 = %q, want 独立重算值", f.SHA256)
	}
	if f.Path != src || f.Size != 11 || f.Mode != "0644" || !f.Explicit || f.Floor {
		t.Errorf("FileEntry = %+v", f)
	}
	if f.MTime.IsZero() {
		t.Error("MTime = zero, want 文件 mtime")
	}
}

// 2. 嵌套目录结构镜像：include 一堵墙，子树完整复制。
func TestSyncNestedDirsMirror(t *testing.T) {
	e, home, root := newTestEngine(t)
	deep := filepath.Join(home, "app", "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := writeFile(t, deep, "file.txt", "deep")

	res := syncOK(t, e, []PkgPlan{{Name: "app", Includes: []Include{{Path: filepath.Join(home, "app"), Explicit: true}}}}, syncT0)
	dst := filepath.Join(root, res.SnapshotID, mirror(src))
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("嵌套镜像缺失: %v", err)
	}
}

// 3. exclude 洞：不复制、不进 manifest，目录洞整棵跳过。
func TestSyncExcludeHole(t *testing.T) {
	e, home, root := newTestEngine(t)
	wall := filepath.Join(home, "app")
	writeFile(t, wall, "keep.txt", "k")
	if err := os.MkdirAll(filepath.Join(wall, "drop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := writeFile(t, filepath.Join(wall, "drop"), "secret.txt", "s")

	res := syncOK(t, e, []PkgPlan{{
		Name:     "app",
		Includes: []Include{{Path: wall, Explicit: true}},
		Excludes: []string{filepath.Join(wall, "drop")},
	}}, syncT0)

	if res.FilesTotal != 1 {
		t.Errorf("FilesTotal = %d, want 1（洞整棵跳过）", res.FilesTotal)
	}
	if _, err := os.Lstat(filepath.Join(root, res.SnapshotID, mirror(secret))); !os.IsNotExist(err) {
		t.Errorf("洞内文件被复制了: %v", err)
	}
	m := readManifest(t, root, res.SnapshotID)
	for _, f := range m.Files {
		if f.Path == secret {
			t.Error("洞内文件进了 manifest")
		}
	}
}

// 4. symlink 复制为同目标链接（读回验证）；断链复制且标 broken。
func TestSyncSymlinks(t *testing.T) {
	e, home, root := newTestEngine(t)
	target := writeFile(t, home, "real.txt", "x")
	link := filepath.Join(home, "alias.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	broken := filepath.Join(home, "dangling")
	if err := os.Symlink("missing-relative", broken); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	res := syncOK(t, e, []PkgPlan{{Name: "app", Includes: []Include{
		{Path: link, Explicit: true}, {Path: broken, Explicit: true},
	}}}, syncT0)
	if res.FilesTotal != 2 {
		t.Fatalf("FilesTotal = %d, want 2", res.FilesTotal)
	}

	// 读回验证：快照里的链接是同款链接，不是目标内容。
	gotTarget, err := os.Readlink(filepath.Join(root, res.SnapshotID, mirror(link)))
	if err != nil {
		t.Fatalf("readlink snapshot: %v", err)
	}
	if gotTarget != target {
		t.Errorf("快照链接目标 = %q, want 原目标 %q（按链接复制，永不跟随）", gotTarget, target)
	}
	info, err := os.Lstat(filepath.Join(root, res.SnapshotID, mirror(link)))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("快照内 %s 不是 symlink", link)
	}

	m := readManifest(t, root, res.SnapshotID)
	byPath := map[string]FileEntry{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	if !byPath[broken].Broken {
		t.Error("断链 Broken = false, want true（照存并标记，不猜目标去哪了）")
	}
	if byPath[link].Broken {
		t.Error("活链 Broken = true, want false")
	}
}

// 5. 显式 include 的底线文件：复制、floor+explicit 为真、不进 FloorIncluded
// （显式条目 = 用户已确认，DESIGN §4）。
func TestSyncExplicitFloorNotReported(t *testing.T) {
	e, home, root := newTestEngine(t)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := writeFile(t, ssh, "id_rsa", "x")

	res := syncOK(t, e, []PkgPlan{{Name: "ssh", Includes: []Include{{Path: key, Explicit: true}}}}, syncT0)
	if res.FilesTotal != 1 {
		t.Fatalf("FilesTotal = %d, want 1", res.FilesTotal)
	}
	if len(res.FloorIncluded) != 0 {
		t.Errorf("FloorIncluded = %v, want 空（显式条目 = 已确认）", res.FloorIncluded)
	}
	m := readManifest(t, root, res.SnapshotID)
	if !m.Files[0].Floor || !m.Files[0].Explicit {
		t.Errorf("FileEntry = %+v, want floor+explicit 双真", m.Files[0])
	}
	if _, err := os.Stat(filepath.Join(root, res.SnapshotID, mirror(key))); err != nil {
		t.Errorf("显式底线文件没被复制（照常复制是本次拍板）: %v", err)
	}
}

// 6. 目录内扫到的底线文件：复制、进 FloorIncluded——先报告，再决断。
func TestSyncFloorIncludedFromScan(t *testing.T) {
	e, home, _ := newTestEngine(t)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := writeFile(t, ssh, "id_rsa", "x")

	res := syncOK(t, e, []PkgPlan{{Name: "ssh", Includes: []Include{{Path: ssh, Explicit: true}}}}, syncT0)
	if len(res.FloorIncluded) != 1 || res.FloorIncluded[0] != key {
		t.Fatalf("FloorIncluded = %v, want [%s]", res.FloorIncluded, key)
	}
}

// 7. id_ed25519.pub 豁免在引擎侧同样生效（MatchFloor 只有一份判定者）。
func TestSyncPubKeyExempt(t *testing.T) {
	e, home, _ := newTestEngine(t)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, ssh, "id_ed25519.pub", "ssh-ed25519 AAAA")

	res := syncOK(t, e, []PkgPlan{{Name: "ssh", Includes: []Include{{Path: ssh, Explicit: true}}}}, syncT0)
	if res.FilesTotal != 1 {
		t.Fatalf("FilesTotal = %d, want 1", res.FilesTotal)
	}
	if len(res.FloorIncluded) != 0 {
		t.Errorf("FloorIncluded = %v, want 空（公钥不是秘密）", res.FloorIncluded)
	}
	m := readManifest(t, e.root, res.SnapshotID)
	if m.Files[0].Floor {
		t.Error("公钥 Floor = true, want false")
	}
}

// 8. 无变化二次 sync → diff 全零。
func TestSyncNoChangeZeroDiff(t *testing.T) {
	e, home, _ := newTestEngine(t)
	writeFile(t, home, "a.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: filepath.Join(home, "a.txt"), Explicit: true}}}}

	syncOK(t, e, plan, syncT0)
	res := syncOK(t, e, plan, syncT1)
	if res.Diff != (DiffStats{}) {
		t.Errorf("diff = %+v, want 全零", res.Diff)
	}
}

// 9. 改文件 → modified=1、sha 更新。
func TestSyncModified(t *testing.T) {
	e, home, root := newTestEngine(t)
	src := writeFile(t, home, "a.txt", "v1")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: src, Explicit: true}}}}

	syncOK(t, e, plan, syncT0)
	m0 := readManifest(t, root, "20261124T150000Z")

	if err := os.WriteFile(src, []byte("v2 changed"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	res := syncOK(t, e, plan, syncT1)
	if res.Diff.Modified != 1 || res.Diff.Added != 0 || res.Diff.Deleted != 0 {
		t.Errorf("diff = %+v, want modified=1", res.Diff)
	}

	m1 := readManifest(t, root, res.SnapshotID)
	if m1.Files[0].SHA256 == m0.Files[0].SHA256 {
		t.Error("内容变了 sha 没变")
	}
	got, _ := os.ReadFile(filepath.Join(root, res.SnapshotID, mirror(src)))
	if string(got) != "v2 changed" {
		t.Errorf("快照内容 = %q, want 更新后的内容", got)
	}
}

// 10. 删文件 → deleted=1（scope 收缩与 exclude 增加由此体现）。
func TestSyncDeleted(t *testing.T) {
	e, home, _ := newTestEngine(t)
	src := writeFile(t, home, "gone.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: src, Explicit: true}}}}

	syncOK(t, e, plan, syncT0)
	if err := os.Remove(src); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	res := syncOK(t, e, plan, syncT1)
	if res.Diff.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", res.Diff.Deleted)
	}
	// 源缺失本身是 warning，不是失败。
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "source missing") {
		t.Errorf("Warnings = %v, want source missing 报告", res.Warnings)
	}
}

// 11. 加文件 → added=1。
func TestSyncAdded(t *testing.T) {
	e, home, _ := newTestEngine(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "a.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: dir, Explicit: true}}}}

	syncOK(t, e, plan, syncT0)
	writeFile(t, dir, "new.txt", "y")
	res := syncOK(t, e, plan, syncT1)
	if res.Diff.Added != 1 {
		t.Errorf("Added = %d, want 1", res.Diff.Added)
	}
}

// 12. 每次成功 sync 顶掉上一份：三次 sync → 只剩最新一个快照目录
// （单快照：PDP 不是版本管理工具，用户要历史让 LLM 维护多份清单）。
func TestSyncReplacesOldSnapshot(t *testing.T) {
	e, home, _ := newTestEngine(t)
	writeFile(t, home, "a.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: filepath.Join(home, "a.txt"), Explicit: true}}}}

	syncOK(t, e, plan, syncT0)
	syncOK(t, e, plan, syncT1)
	syncOK(t, e, plan, syncT2)
	if got := snapshotDirCount(t, e); got != 1 {
		t.Errorf("快照目录数 = %d, want 1（单快照）", got)
	}
	ids, _ := e.snapshotIDs()
	if ids[0] != "20261124T150010Z" {
		t.Errorf("保留的快照 = %v, want 最新一个", ids)
	}
}

// 13. chmod 000 的文件 → 进 SkippedError、其余照常、快照仍成功。
func TestSyncSkippedError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位约束")
	}
	e, home, _ := newTestEngine(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	blocked := writeFile(t, dir, "blocked.txt", "x")
	t.Cleanup(func() { os.Chmod(blocked, 0o644) })
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	writeFile(t, dir, "ok.txt", "y")

	res := syncOK(t, e, []PkgPlan{{Name: "app", Includes: []Include{{Path: dir, Explicit: true}}}}, syncT0)
	if res.FilesTotal != 1 {
		t.Errorf("FilesTotal = %d, want 1（读得了的那份）", res.FilesTotal)
	}
	if len(res.SkippedError) != 1 || !strings.Contains(res.SkippedError[0], "blocked.txt") {
		t.Errorf("SkippedError = %v, want 点名 blocked.txt", res.SkippedError)
	}
}

// 14. 原子性：root 置只读后 sync 失败 → 旧快照原样、无 .tmp 残留。
func TestSyncAtomicFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位约束")
	}
	e, home, root := newTestEngine(t)
	writeFile(t, home, "a.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: filepath.Join(home, "a.txt"), Explicit: true}}}}
	first := syncOK(t, e, plan, syncT0)

	// root 去掉写位：staging 目录建不起来，sync 必败。
	t.Cleanup(func() { os.Chmod(root, 0o755) })
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	if _, err := e.Sync(context.Background(), plan, 7, syncT1, false); err == nil {
		t.Fatal("Sync() error = nil, want 失败（staging 建不起来）")
	}

	// 旧快照一个字节不动。
	if _, err := os.Stat(filepath.Join(root, first.SnapshotID, "manifest.json")); err != nil {
		t.Errorf("旧快照被破坏: %v", err)
	}
	// 无 .tmp 残留。
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir root: %v", err)
	}
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".tmp-") {
			t.Errorf(".tmp 残留: %s", en.Name())
		}
	}
}

// 15. dry_run → 零目录零文件、diff 正确、SnapshotID 空。
func TestSyncDryRun(t *testing.T) {
	e, home, root := newTestEngine(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "a.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: dir, Explicit: true}}}}

	syncOK(t, e, plan, syncT0) // 先有一份真实快照
	writeFile(t, dir, "new.txt", "y")

	res, err := e.Sync(context.Background(), plan, 7, syncT1, true)
	if err != nil {
		t.Fatalf("dry-run Sync() error = %v", err)
	}
	if res.SnapshotID != "" {
		t.Errorf("SnapshotID = %q, want 空", res.SnapshotID)
	}
	if res.Diff.Added != 1 || res.Diff.Modified != 0 || res.Diff.Deleted != 0 {
		t.Errorf("diff = %+v, want added=1（dry_run 也要算得出 diff）", res.Diff)
	}
	if res.FilesTotal != 2 {
		t.Errorf("FilesTotal = %d, want 2（遍历 + 哈希照做）", res.FilesTotal)
	}
	if got := snapshotDirCount(t, e); got != 1 {
		t.Errorf("快照目录数 = %d, want 1（dry_run 零写入）", got)
	}
	entries, _ := os.ReadDir(root)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".tmp-") || en.Name() == "20261124T150005Z" {
			t.Errorf("dry_run 留下了 %s", en.Name())
		}
	}
}

// 16. 同秒两次 sync → id 冲突加 -2 后缀；单快照下第二次提交
// 顶掉第一次，root 只剩 -2 那份。
func TestSyncSameSecondIDConflict(t *testing.T) {
	e, home, _ := newTestEngine(t)
	writeFile(t, home, "a.txt", "x")
	plan := []PkgPlan{{Name: "app", Includes: []Include{{Path: filepath.Join(home, "a.txt"), Explicit: true}}}}

	first := syncOK(t, e, plan, syncT0)
	second := syncOK(t, e, plan, syncT0) // 同一时刻
	if second.SnapshotID != first.SnapshotID+"-2" {
		t.Errorf("第二次 SnapshotID = %q, want %q", second.SnapshotID, first.SnapshotID+"-2")
	}
	if got := snapshotDirCount(t, e); got != 1 {
		t.Errorf("快照目录数 = %d, want 1（单快照：第二次顶掉第一次）", got)
	}
	ids, _ := e.snapshotIDs()
	if ids[0] != second.SnapshotID {
		t.Errorf("保留的快照 = %v, want %q", ids, second.SnapshotID)
	}
}

// 17. 墙重叠（补丁 #15b）：同一目录被两堵墙覆盖 → 每文件只记一行、
// 只复制一次；深墙先走——显式点名的私钥不因 plan 顺序被宽墙扫成
// “未确认”而误报进 FloorIncluded。
func TestOverlappingWalls(t *testing.T) {
	e, home, root := newTestEngine(t)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := writeFile(t, ssh, "id_rsa", "x")
	writeFile(t, ssh, "known_hosts", "x")

	// 故意把宽墙排在显式条目前面——深墙先行的排序应纠正顺序。
	plan := []PkgPlan{
		{Name: "broad", Includes: []Include{{Path: ssh, Explicit: true}}},
		{Name: "narrow", Includes: []Include{{Path: key, Explicit: true}}},
	}
	res := syncOK(t, e, plan, syncT0)
	if res.FilesTotal != 2 {
		t.Errorf("FilesTotal = %d, want 2（重叠不双记）", res.FilesTotal)
	}
	if len(res.FloorIncluded) != 0 {
		t.Errorf("FloorIncluded = %v, want 空（显式条目先于宽墙的扫描落账）", res.FloorIncluded)
	}
	m := readManifest(t, root, res.SnapshotID)
	if len(m.Files) != 2 {
		t.Fatalf("manifest 行数 = %d, want 2（无重复行）", len(m.Files))
	}
	for _, f := range m.Files {
		if f.Path == key && !f.Explicit {
			t.Error("显式点名的私钥被记成了扫描身份（深墙先走失效）")
		}
	}
}

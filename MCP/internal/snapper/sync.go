package snapper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Sync 执行一次快照：遍历 plan → 复制到 staging → 写 manifest →
// 整目录 rename 提交 → 替换旧快照。
//
// 原子性（DESIGN §5）：全部复制到 <root>/.tmp-<id>/ → 每个文件 Sync →
// manifest 落盘并 Sync → Rename 为 <id>/ → root 目录 Sync → 替换旧快照。
// 任何一步失败：删掉整个 .tmp，旧快照一个字节不动——
// 快照完整构建成功后才替换旧快照，失败或中断不伤旧账。
//
// diff 与“替换后仍存在的最新 manifest”全量比对——即 sync 开始前
// 的最新快照（本次 <id> 与它并列为保留的前两份）。
// dryRun：遍历 + 哈希（读但不写），不建任何目录，SnapshotID 为空。
//
// ctx 目前不参与（遍历由文件树大小决定，取消点留待真实需求），
// 仅为将来保留接口形状。
func (e *Engine) Sync(ctx context.Context, plan []PkgPlan, listRev int, now time.Time, dryRun bool) (Result, error) {
	prev, err := e.latestManifest()
	if err != nil {
		return Result{}, err // 旧账读不出来是真实故障，如实上抛
	}

	id := now.UTC().Format(snapshotTimeLayout)
	tmpRoot := ""
	if !dryRun {
		id, err = e.unusedID(id)
		if err != nil {
			return Result{}, err
		}
		tmpRoot = filepath.Join(e.root, ".tmp-"+id)
		if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
			return Result{}, fmt.Errorf("create staging dir: %w", err)
		}
		// 失败清场：rename 成功后 tmpRoot 已不存在，RemoveAll 是 no-op，
		// 所以无条件 defer 是安全的；失败路径半成品绝不留在用户目录。
		defer os.RemoveAll(tmpRoot)
	}

	// 洞跨包合并——洞优先于墙（重叠 scope 的执行语义，见 walker.excluded）。
	excludes := []string{}
	for _, pkg := range plan {
		excludes = append(excludes, pkg.Excludes...)
	}
	// 墙也展平，并按“更深者先走”排序：重叠时（宽墙目录 vs 深墙条目）
	// 具体身份先落账——scope 显式点名的私钥不会因 plan 顺序被宽墙的
	// 扫描抢记成“未确认”。互不重叠的墙顺序无关，排序对它们无影响。
	includes := []Include{}
	for _, pkg := range plan {
		includes = append(includes, pkg.Includes...)
	}
	slices.SortStableFunc(includes, func(a, b Include) int {
		return len(b.Path) - len(a.Path)
	})
	w := newWalker(e, tmpRoot, dryRun, excludes)
	for _, inc := range includes {
		w.walkInclude(inc)
	}

	// manifest 字段按路径排序：人可读，diff 与测试都拿到稳定序。
	slices.SortFunc(w.files, func(a, b FileEntry) int {
		return strings.Compare(a.Path, b.Path)
	})

	res := Result{
		Diff:          diffStats(prev, w.byPath),
		FloorIncluded: w.floorIncluded,
		SkippedError:  w.skipped,
		Warnings:      w.warnings,
		FilesTotal:    len(w.files),
	}
	if dryRun {
		return res, nil // 读而不写，到此为止
	}

	if err := saveManifest(filepath.Join(tmpRoot, "manifest.json"), &Manifest{
		SnapshotID: id,
		CreatedAt:  now,
		ListRev:    listRev,
		Files:      w.files,
	}); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmpRoot, filepath.Join(e.root, id)); err != nil {
		return Result{}, fmt.Errorf("commit snapshot %s: %w", id, err)
	}
	if err := syncDir(e.root); err != nil {
		return Result{}, err
	}

	// 提交之后才替换旧快照：失败不伤旧账的顺序保证。
	rotateWarns, err := e.rotate()
	if err != nil {
		return Result{}, err // 读快照目录失败是真实故障
	}
	res.Warnings = append(res.Warnings, rotateWarns...)

	res.SnapshotID = id
	return res, nil
}

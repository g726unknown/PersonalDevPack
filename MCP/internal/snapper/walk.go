package snapper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/g726unknown/pdp/internal/pathobserver"
)

// walker 是一次 Sync 的遍历与复制状态。
// 所有写入以 dryRun 为开关：dryRun 只读（遍历 + 哈希），零目录零文件。
type walker struct {
	e       *Engine
	tmpRoot string // dryRun 时为空
	dryRun  bool
	exclude []string // 全部包的洞合并——洞优先于墙，跨包生效

	files         []FileEntry
	byPath        map[string]FileEntry
	floorIncluded []string
	skipped       []string
	warnings      []string
}

func newWalker(e *Engine, tmpRoot string, dryRun bool, excludes []string) *walker {
	return &walker{
		e:             e,
		tmpRoot:       tmpRoot,
		dryRun:        dryRun,
		exclude:       excludes,
		files:         []FileEntry{},
		byPath:        map[string]FileEntry{},
		floorIncluded: []string{},
		skipped:       []string{},
		warnings:      []string{},
	}
}

// excluded 报告 p 是否严格落在任一洞之下。
// 洞优先于墙：路径既被某墙点名又落在另一堵墙的洞里时，不复制——
// 重叠 scope 的歧义不属于执行层，报告而不猜测，交回对话层。
func (w *walker) excluded(p string) bool {
	for _, e := range w.exclude {
		if strings.HasPrefix(p, e+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// walkInclude 处理一条 include：根条目显式（scope 点名），
// 目录展开的后代全部非显式——FloorIncluded 报告的分界依据。
func (w *walker) walkInclude(inc Include) {
	if w.excluded(inc.Path) {
		w.warnings = append(w.warnings, fmt.Sprintf("include %s is shadowed by an exclude; skipped", inc.Path))
		return
	}
	fi, err := os.Lstat(inc.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 源不存在：存档与现实的偏差如实报告，不算失败。
			w.warnings = append(w.warnings, fmt.Sprintf("source missing: %s", inc.Path))
			return
		}
		w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", inc.Path, err))
		return
	}
	switch {
	case fi.IsDir():
		w.walkDir(inc)
	case fi.Mode()&os.ModeSymlink != 0:
		w.copySymlink(inc.Path, true)
	case fi.Mode().IsRegular():
		w.copyFile(inc.Path, true)
	default:
		w.warnings = append(w.warnings, fmt.Sprintf("non-regular file skipped: %s", inc.Path))
	}
}

// walkDir 递归一堵墙。单点失败记 skipped 不废整棵树；
// 洞上的目录整棵跳过（fs.SkipDir）。
func (w *walker) walkDir(inc Include) {
	root := inc.Path
	// fn 永不返回错误（失败就地记账），WalkDir 只在根出错时返回 err。
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", p, err))
			return nil
		}
		if p == root {
			return nil // 墙根本身不记 manifest；镜像目录随文件写入 MkdirAll 建立
		}
		if w.excluded(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			w.copySymlink(p, false)
		case d.Type().IsRegular():
			w.copyFile(p, false)
		default:
			w.warnings = append(w.warnings, fmt.Sprintf("non-regular file skipped: %s", p))
		}
		return nil
	})
}

// copyFile 复制一个 regular file：io.TeeReader 把复制流同时喂 SHA-256，
// 一遍过——内容只在 磁盘→进程内存→磁盘 的字节管道里流动，
// 哈希之外任何一层都拿不到内容（DESIGN §1：内容永不进模型上下文）。
func (w *walker) copyFile(src string, explicit bool) {
	if _, seen := w.byPath[src]; seen {
		return // 墙重叠：先到的墙已覆盖（深墙先行的排序保证具体身份先落账）
	}
	fi, err := os.Lstat(src)
	if err != nil {
		w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
		return
	}
	in, err := os.Open(src) // 本包允许 Open 用户文件的唯一正当理由：复制
	if err != nil {
		w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
		return
	}
	defer in.Close()

	h := sha256.New()
	if w.dryRun {
		if _, err := io.Copy(io.Discard, io.TeeReader(in, h)); err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
	} else {
		dst := filepath.Join(w.tmpRoot, mirror(src))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
		if _, err := io.Copy(out, io.TeeReader(in, h)); err != nil {
			out.Close()
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return // 半个文件留在 tmp 里——tmp 整体要么 rename 要么 RemoveAll
		}
		if err := out.Sync(); err != nil {
			out.Close()
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
		if err := out.Close(); err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
		// mtime 尽力而为：保不住就 warning，不废复制。
		if err := os.Chtimes(dst, fi.ModTime(), fi.ModTime()); err != nil {
			w.warnings = append(w.warnings, fmt.Sprintf("mtime not preserved: %s", src))
		}
	}

	w.record(FileEntry{
		Path:     src,
		Size:     fi.Size(),
		Mode:     fmt.Sprintf("%04o", uint32(fi.Mode().Perm())),
		MTime:    fi.ModTime(),
		SHA256:   hex.EncodeToString(h.Sum(nil)),
		Explicit: explicit,
	}, explicit)
}

// copySymlink 按链接复制：Readlink 取目标字符串、快照里 Symlink
// 重建同款链接。永不 Open 链接、永不跟随目标内容——目标内容若也
// 要备份，目标路径须在 scope 里显式 include（DESIGN §5）。
// 断链照存并标记（broken 事实进 manifest，不猜目标去哪了）。
func (w *walker) copySymlink(src string, explicit bool) {
	if _, seen := w.byPath[src]; seen {
		return // 墙重叠：先到先得，同上
	}
	target, err := os.Readlink(src)
	if err != nil {
		w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
		return
	}
	broken := false
	// os.Stat 仅判定目标存在性（pathobserver 同款白名单用途），不读内容。
	if _, err := os.Stat(src); err != nil {
		broken = true
	}
	fi, err := os.Lstat(src)
	if err != nil {
		w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
		return
	}
	if !w.dryRun {
		dst := filepath.Join(w.tmpRoot, mirror(src))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
		if err := os.Symlink(target, dst); err != nil {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: %v", src, err))
			return
		}
	}

	w.record(FileEntry{
		Path:     src,
		Mode:     fmt.Sprintf("%04o", uint32(fi.Mode().Perm())),
		MTime:    fi.ModTime(),
		Target:   target,
		Broken:   broken,
		Explicit: explicit,
	}, explicit)
}

// record 收账：底线判定只有一份判定者（pathobserver.MatchFloor）。
// 命中且非显式 → FloorIncluded 点名——先报告，再决断（本次拍板）。
func (w *walker) record(entry FileEntry, explicit bool) {
	entry.Floor = pathobserver.MatchFloor(entry.Path, w.e.home)
	w.files = append(w.files, entry)
	w.byPath[entry.Path] = entry
	if entry.Floor && !explicit {
		w.floorIncluded = append(w.floorIncluded, entry.Path)
	}
}

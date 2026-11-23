// Package recorder 负责软件清单文件（PDP_HOME/list.json）的读写，
// 是 PDP 中唯一写盘的层。Load 是读路径，Update 是唯一写入口。
package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// CurrentVersion 是 list.json 的当前格式版本。
// Load 只认识这个版本；升级格式时在此递增，并另行处理迁移。
const CurrentVersion = 1

// List 是清单文件 list.json 的内存形态。
type List struct {
	Version   int                 `json:"version"`
	Revision  int                 `json:"revision"`
	UpdatedAt time.Time           `json:"updated_at"`
	Packages  map[string]*Package `json:"packages"`
}

// Package 是清单中的一个软件条目。
//
// RemovedAt 用 *time.Time + omitempty 表达软删除：
// 未移除时字段完全不出现，而不是序列化出 0001 年的零值时间。
type Package struct {
	Name       string     `json:"name"`
	Source     string     `json:"source"`
	Version    string     `json:"version"`
	Type       string     `json:"type"`
	RecordedAt time.Time  `json:"recorded_at"`
	CheckedAt  time.Time  `json:"checked_at"`
	RemovedAt  *time.Time `json:"removed_at,omitempty"`
}

// Recorder 持有清单所在的状态目录。
// 它是无状态的：Load 每次都从盘上读最新事实；
// Open 不建目录、不碰文件。
type Recorder struct {
	dir string
}

// Open 返回指向状态目录 dir 的 Recorder。
// 创建目录、文件锁与写入属于 Save 的职责。
func Open(dir string) *Recorder {
	return &Recorder{dir: dir}
}

// Load 从 dir/list.json 读出清单。
//
//   - 文件不存在：返回空清单（Version=CurrentVersion、Revision=0、
//     Packages 为非 nil 的空 map）；
//   - JSON 损坏（包括 0 字节）或 version 不是 CurrentVersion：
//     返回错误，不修复、不覆盖——把坏文件当空清单返回，
//     下一次 Save 就会永久覆盖用户数据。
//
// Load 返回的 List.Packages 永远非 nil：合法 JSON 缺 packages 键时
// 在此归一化为空 map，调用方不必记例外。
//
// ctx 目前不参与读取（单文件读快到无需取消），仅为将来保留接口形状。
func (r *Recorder) Load(ctx context.Context) (*List, error) {
	path := filepath.Join(r.dir, "list.json")

	raw, err := os.ReadFile(path)
	if err != nil {
		// 只有“文件不存在”是合法的空状态；权限错误、路径是目录
		// 等其他读错误如实上报，否则它们会被静默当成空清单。
		if errors.Is(err, fs.ErrNotExist) {
			return &List{
				Version:  CurrentVersion,
				Revision: 0,
				Packages: map[string]*Package{},
			}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var l List
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if l.Version != CurrentVersion {
		return nil, fmt.Errorf("parse %s: unsupported format version %d (expected %d)",
			path, l.Version, CurrentVersion)
	}
	if l.Packages == nil {
		l.Packages = map[string]*Package{}
	}
	return &l, nil
}

// ---------------------------------------------------------------------------
// 写路径
// ---------------------------------------------------------------------------

// ErrConflict 是 *ConflictError 的判定基准：errors.Is(err, ErrConflict)
// 为真表示 baseRev 已过期，调用方应重新 Load 后再试，不允许直接覆盖。
var ErrConflict = errors.New("revision conflict")

// ConflictError 表示 Update 的 baseRev 与锁内重读到的 Revision 不符。
// Current 是盘上的最新值，供调用方基于它重试。
type ConflictError struct{ Current int }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("revision conflict: current revision is %d", e.Current)
}

// Unwrap 让 errors.Is(err, ErrConflict) 成立。
func (e *ConflictError) Unwrap() error { return ErrConflict }

// Update 是清单的唯一写入口，顺序固定：
//
//	加锁 → 锁内重读最新 → 校验 baseRev → fn 修改 →
//	框架自增 Revision、刷新 UpdatedAt → 原子落盘 → 解锁
//
// fn 只允许改业务字段（Packages 里的条目）；Revision 与 UpdatedAt 由框架
// 写，不信任 fn。fn 返回错误则立即中止、什么都不写，fn 的错误原样返回，
// 不包裹，保持调用方可用自己的 sentinel / errors.Is 判别。
// baseRev 过期返回 *ConflictError，用 errors.Is(err, ErrConflict) 判别。
//
// 并发安全来自两件事：flock 保证同一时刻只有一个进程在写；锁内重新 Load
// 保证校验的 revision 是此刻盘上的最新值，不是调用方手里的快照。
// 崩溃安全来自 save 的原子落盘。
//
// ctx 目前只透传给 Load。
func (r *Recorder) Update(ctx context.Context, baseRev int, fn func(*List) error) (*List, error) {
	release, err := r.acquireLock()
	if err != nil {
		return nil, err
	}
	defer release()

	l, err := r.Load(ctx)
	if err != nil {
		return nil, err
	}
	if l.Revision != baseRev {
		return nil, &ConflictError{Current: l.Revision}
	}
	if err := fn(l); err != nil {
		return nil, err
	}

	// 框架字段由框架写：fn 动过也不算数。
	l.Revision++
	l.UpdatedAt = time.Now()

	if err := r.save(l); err != nil {
		return nil, err
	}
	return l, nil
}

// acquireLock 在状态目录的 .list.lock 上取得排它锁并返回释放函数。
//
// 锁加在单独的 .list.lock 上而不是 list.json 本身：原子写用 rename 顶掉
// 旧文件，之后锁 list.json 锁到的是旧 inode，而别的进程打开同一路径锁到
// 的是新 inode——锁就失效了。.list.lock 从不 rename，永远指向同一 inode。
// flock 随进程退出由内核自动释放，不存在残留死锁。阻塞等待是预期行为：
// 并发写的窗口是毫秒级。
//
// 首次写盘时状态目录可能还不存在（Open 不建目录），在这里补建；
// 锁文件与临时文件都需要它。
func (r *Recorder) acquireLock() (release func(), err error) {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir %s: %w", r.dir, err)
	}
	lockPath := filepath.Join(r.dir, ".list.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // 尽力而为；Close 本身也会释放
		f.Close()
	}, nil
}

// save 原子地把 l 写到 dir/list.json。目录已由 acquireLock 建好。
// 落盘顺序固定，每一步都有理由，调整前先想清楚：
//
//		同目录建临时文件 → 编码 → 写入 + Chmod(0600) → Sync → Close →
//		Rename → 目录 Sync
//
//	  - 临时文件与目标同目录：rename(2) 只在同一文件系统内原子，
//	    跨分区会退化成“复制+删除”，中途崩溃就留下半个文件。
//	  - Rename 前先 Sync 文件：数据先于目录项变更落盘，掉电后至多是旧文件。
//	  - Rename 后同步目录：让“换名”这个目录项变更也持久化。
//
// 任何一步失败，defer 都会清掉临时文件；已成功 Rename 则不再清理。
func (r *Recorder) save(l *List) error {
	tmp, err := os.CreateTemp(r.dir, ".list-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", r.dir, err)
	}
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(tmp.Name()) // 半成品无害，但别留在用户目录里
		}
	}()

	data, err := json.MarshalIndent(l, "", "  ") // 人可读，不用 json.Marshal
	if err != nil {
		return fmt.Errorf("encode list: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}

	path := filepath.Join(r.dir, "list.json")
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp.Name(), path, err)
	}
	renamed = true

	d, err := os.Open(r.dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", r.dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync dir %s: %w", r.dir, err)
	}
	return nil
}

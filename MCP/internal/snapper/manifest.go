package snapper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// saveManifest 把账本原子落盘，纪律照抄 recorder.save：
// 写入 → Chmod(0600) → Sync → Close。调用方负责随后的整目录 rename。
func saveManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ") // 人可读，不用 json.Marshal
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// syncDir 让目录项变更（rename 顶掉旧名）也持久化——
// recorder.save 的收尾步骤，掉电后至多是旧快照，不会出现半个新名。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

// rotate 替换旧快照：只保留最新一份（单快照，拍板修订——
// PDP 不是版本管理工具，快照是存档判断的投影不是历史账本；
// 用户要历史版本，让 LLM 维护多份清单或多个 PDP_HOME）。
// 删除失败进 warnings——清理问题不废掉一次成功的备份，
// 下一次 sync 还会再试删。
func (e *Engine) rotate() ([]string, error) {
	ids, err := e.snapshotIDs()
	if err != nil {
		return nil, err
	}
	var warns []string
	// ids 升序，末位即本次刚提交的最新快照，其余全部删除。
	for _, old := range ids[:len(ids)-1] {
		if err := os.RemoveAll(filepath.Join(e.root, old)); err != nil {
			warns = append(warns, fmt.Sprintf("rotate: remove old snapshot %s: %v", old, err))
		}
	}
	return warns, nil
}

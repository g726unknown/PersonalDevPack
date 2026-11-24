package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/g726unknown/pdp/internal/pathobserver"
)

// InspectInput 是 InspectPaths 的调用参数。
type InspectInput struct {
	Paths      []string
	MaxEntries int // 目录采样上限；0 → pathobserver 默认 50；负数拒绝
}

// InspectResult 是 InspectPaths 的输出。
type InspectResult struct {
	// Entries 直接透传 pathobserver.Facts——Facts 本身就是线上形态
	//（precedent：SystemInfo 直通；mcpserver 为类型名 import
	// pathobserver，与它已 import recorder 同理）。
	Entries []pathobserver.Facts
	// Warnings 是口径层给 LLM 的文字提示（见 InspectPaths 文档）。
	Warnings []string
}

// InspectPaths 批量探查路径元数据，是备份范围判断的证据来源。
// 只读：不碰清单，也不被清单影响。
//
// 预检在探查之前，口径是整批拒绝（一条非法拖垮全批才对——
// 与其探一半再拒，不如先验完）：任一路径为空、相对、~user 形态
// → ErrInvalidInput；MaxEntries 负数同罪。
// 空 Paths 是合法的空请求 → 空结果，不花一次 syscall。
//
// warnings 组装是本用例真正的口径职责：探查失败、敏感底线命中、
// 采样子命中各生成一条措辞清晰的 warning——LLM 靠这些文字决定
// 要不要就此询问用户，而不是靠解析 Facts 字段自行推断。
func (s *Service) InspectPaths(ctx context.Context, in InspectInput) (InspectResult, error) {
	if in.MaxEntries < 0 {
		return InspectResult{}, fmt.Errorf("%w: negative max_entries %d", ErrInvalidInput, in.MaxEntries)
	}
	for _, p := range in.Paths {
		if !validInspectPath(p) {
			return InspectResult{}, fmt.Errorf("%w: path %q must be absolute or start with ~/ (no ~user form)", ErrInvalidInput, p)
		}
	}

	if len(in.Paths) == 0 {
		// 空请求：非 nil 空形态（项目约定），不探查。
		return InspectResult{Entries: []pathobserver.Facts{}, Warnings: []string{}}, nil
	}

	inputs := make([]pathobserver.Input, 0, len(in.Paths))
	for _, p := range in.Paths {
		inputs = append(inputs, pathobserver.Input{Path: p, MaxEntries: in.MaxEntries})
	}
	entries := s.pobs.Probe(ctx, inputs)

	warnings := []string{}
	for _, f := range entries {
		if f.Error != "" {
			warnings = append(warnings, fmt.Sprintf("probe failed for %s: %s", f.Path, f.Error))
		}
		if f.SensitiveFloor {
			warnings = append(warnings, fmt.Sprintf("%s hits the sensitive floor and will never be backed up silently", f.Path))
		}
		for _, name := range f.SensitiveEntries {
			warnings = append(warnings, fmt.Sprintf("sampled sensitive file in %s: %s", f.Path, name))
		}
	}
	return InspectResult{Entries: entries, Warnings: warnings}, nil
}

// validInspectPath 是路径预检：绝对或 ~/ 前缀；~user 形态拒绝。
// 与 recorder.SetScope 的路径口径一致。双层的意义在分工：
// 这里整批拒绝（信封 rejected、探查不发生），pathobserver 单条
// 自保（未来新调用路径漏过预检也碰不到真实文件）。
func validInspectPath(p string) bool {
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "~") {
		return p == "~" || strings.HasPrefix(p, "~/")
	}
	return filepath.IsAbs(p)
}

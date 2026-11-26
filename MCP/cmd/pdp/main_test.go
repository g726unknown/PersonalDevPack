package main

import (
	"strings"
	"testing"
)

// TestInstructionsHonesty 钉住 instructions 的历次诚实校准：
// 备份执行已实现（阶段三）、恢复改道为宿主 shell 流程（卡 #17）、
// 两代旧的否认句都必须保持死亡。
func TestInstructionsHonesty(t *testing.T) {
	if !strings.Contains(instructions, "Backup execution IS implemented") {
		t.Error("instructions 缺少 \"Backup execution IS implemented\"——阶段三已落地，否认备份就是撒谎")
	}
	if !strings.Contains(instructions, "Restoration is NOT a PDP tool") {
		t.Error("instructions 缺少 \"Restoration is NOT a PDP tool\"——恢复改道后的准确表述必须在场")
	}
	if !strings.Contains(instructions, "cp -a the snapshot back") {
		t.Error("instructions 缺少恢复路径的具体指引（cp -a 回放）")
	}
	if strings.Contains(instructions, "Restoration is NOT implemented") {
		t.Error("instructions 仍含 \"Restoration is NOT implemented\"——改道后这句成了谎话（恢复流程已文档化）")
	}
	if strings.Contains(instructions, "Backup execution and restoration are NOT implemented") {
		t.Error("instructions 仍含旧的“备份与恢复均未实现”整句——它已过时")
	}
}

// TestInstructionsCapabilityFirst 钉住卡 #18 的能力可见性句：
// 状态类问题优先用 PDP 工具（权威通道），别用 shell 从原始文件拼答案。
func TestInstructionsCapabilityFirst(t *testing.T) {
	if !strings.Contains(instructions, "use PDP's tools first") {
		t.Error("instructions 缺少能力优先句（use PDP's tools first）——摩擦修正必须在场")
	}
	if !strings.Contains(instructions, "do not reconstruct state from raw files") {
		t.Error("instructions 缺少禁止从原始文件拼状态的表述")
	}
}

func TestStateDir(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name: "PDP_HOME 优先于一切",
			env:  map[string]string{"PDP_HOME": "/custom/pdp", "XDG_DATA_HOME": "/xdg", "HOME": "/home/u"},
			want: "/custom/pdp",
		},
		{
			name: "XDG_DATA_HOME 次之",
			env:  map[string]string{"XDG_DATA_HOME": "/xdg", "HOME": "/home/u"},
			want: "/xdg/pdp",
		},
		{
			name: "HOME 回退",
			env:  map[string]string{"HOME": "/home/u"},
			want: "/home/u/.local/share/pdp",
		},
		{
			name:    "三者皆无 → error",
			env:     map[string]string{},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := stateDir(func(k string) string { return c.env[k] })
			if c.wantErr {
				if err == nil {
					t.Fatalf("stateDir() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("stateDir() error = %v, want nil", err)
			}
			if got != c.want {
				t.Errorf("stateDir() = %q, want %q", got, c.want)
			}
		})
	}
}

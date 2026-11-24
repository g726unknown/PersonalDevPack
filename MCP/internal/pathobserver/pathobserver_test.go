package pathobserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本包的测试替身就是真实临时目录（探查对象是文件系统，
// 像 recorder 一样不需要任何假实现），零 stub。

func probeOne(t *testing.T, home string, in Input) Facts {
	t.Helper()
	out := New(home).Probe(context.Background(), []Input{in})
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	return out[0]
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// 1. 不存在 → Exists:false，无 Error（事实不是故障）。
func TestProbeMissingPath(t *testing.T) {
	f := probeOne(t, t.TempDir(), Input{Path: "/definitely/not/here"})
	if f.Exists {
		t.Error("Exists = true, want false")
	}
	if f.Error != "" {
		t.Errorf("Error = %q, want empty（不存在是事实不是故障）", f.Error)
	}
	if f.Kind != "" || f.Mode != "" || f.SensitiveFloor || f.EntriesTotal != 0 {
		t.Errorf("Facts = %+v, want zero fields", f)
	}
}

// 2. 文件 → kind/size（精确字节数）/mode/mtime。
func TestProbeFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.txt", "hello") // 5 字节
	// 显式 chmod 消除 umask 的不确定性，Mode 断言才稳定。
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	before := time.Now().Add(-time.Second)

	f := probeOne(t, dir, Input{Path: path})
	if !f.Exists || f.Kind != KindFile {
		t.Fatalf("Facts = %+v, want existing file", f)
	}
	if f.Size != 5 {
		t.Errorf("Size = %d, want 5（精确字节数）", f.Size)
	}
	if f.Mode != "0644" {
		t.Errorf("Mode = %q, want 0644", f.Mode)
	}
	if f.MTime == nil {
		t.Fatal("MTime = nil, want non-nil")
	}
	if f.MTime.Before(before) || f.MTime.After(time.Now().Add(time.Second)) {
		t.Errorf("MTime = %v, want around now", *f.MTime)
	}
	if f.EntriesSample != nil || f.Truncated || f.SensitiveFloor {
		t.Errorf("file Facts 带目录字段: %+v", f)
	}
}

// 3. 目录 → 第一层采样按名字排序、EntriesTotal 正确。
func TestProbeDirSampleSorted(t *testing.T) {
	dir := t.TempDir()
	// 乱序写入，验证输出排序（os.ReadDir 的保证，在此钉住）。
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		writeFile(t, dir, name, "x")
	}

	f := probeOne(t, t.TempDir(), Input{Path: dir})
	if !f.Exists || f.Kind != KindDir {
		t.Fatalf("Facts = %+v, want existing dir", f)
	}
	if f.EntriesTotal != 3 {
		t.Errorf("EntriesTotal = %d, want 3", f.EntriesTotal)
	}
	want := []string{"alpha", "bravo", "charlie"}
	if len(f.EntriesSample) != len(want) {
		t.Fatalf("len(EntriesSample) = %d, want %d", len(f.EntriesSample), len(want))
	}
	for i, n := range want {
		if f.EntriesSample[i] != n {
			t.Errorf("EntriesSample[%d] = %q, want %q（名字升序）", i, f.EntriesSample[i], n)
		}
	}
	if f.Truncated {
		t.Error("Truncated = true, want false")
	}
}

// 4. 5 个文件 + MaxEntries:2 → 采样 2 条、Truncated、EntriesTotal:5。
func TestProbeDirTruncation(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("f%d.txt", i), "x")
	}

	f := probeOne(t, t.TempDir(), Input{Path: dir, MaxEntries: 2})
	if f.EntriesTotal != 5 {
		t.Errorf("EntriesTotal = %d, want 5（总数在采样前）", f.EntriesTotal)
	}
	if len(f.EntriesSample) != 2 {
		t.Fatalf("len(EntriesSample) = %d, want 2", len(f.EntriesSample))
	}
	if f.EntriesSample[0] != "f0.txt" || f.EntriesSample[1] != "f1.txt" {
		t.Errorf("EntriesSample = %v, want [f0.txt f1.txt]（有序前缀）", f.EntriesSample)
	}
	if !f.Truncated {
		t.Error("Truncated = false, want true")
	}
}

// 5. MaxEntries:0 → 默认 50。
func TestProbeDirDefaultMaxEntries(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 55; i++ {
		writeFile(t, dir, fmt.Sprintf("f%02d", i), "x")
	}

	f := probeOne(t, t.TempDir(), Input{Path: dir})
	if len(f.EntriesSample) != 50 {
		t.Errorf("len(EntriesSample) = %d, want 50（默认上限）", len(f.EntriesSample))
	}
	if !f.Truncated {
		t.Error("Truncated = false, want true")
	}
	if f.EntriesTotal != 55 {
		t.Errorf("EntriesTotal = %d, want 55", f.EntriesTotal)
	}
}

// 6. symlink 指向文件 → Kind:symlink、目标为绝对路径、无 entries。
func TestProbeSymlinkToFile(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "real.txt", "x")
	link := filepath.Join(dir, "alias.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	f := probeOne(t, t.TempDir(), Input{Path: link})
	if !f.Exists || f.Kind != KindSymlink {
		t.Fatalf("Facts = %+v, want existing symlink", f)
	}
	if f.SymlinkTarget != target {
		t.Errorf("SymlinkTarget = %q, want %q", f.SymlinkTarget, target)
	}
	if f.SymlinkBroken {
		t.Error("SymlinkBroken = true, want false")
	}
	if f.EntriesSample != nil || f.EntriesTotal != 0 {
		t.Errorf("symlink 不穿透（目标是文件更无从谈起）: %+v", f)
	}
	if f.Size != 0 {
		t.Errorf("Size = %d, want 0（仅文件报 size）", f.Size)
	}
}

// 7. symlink 指向目录 → 同样不穿透：报链接，不列目标内容。
func TestProbeSymlinkToDirDoesNotTraverse(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	writeFile(t, target, "inside.txt", "x")
	link := filepath.Join(dir, "to-dir")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	f := probeOne(t, t.TempDir(), Input{Path: link})
	if f.Kind != KindSymlink {
		t.Fatalf("Kind = %q, want %q", f.Kind, KindSymlink)
	}
	if f.SymlinkTarget != target {
		t.Errorf("SymlinkTarget = %q, want %q", f.SymlinkTarget, target)
	}
	if f.EntriesSample != nil || f.EntriesTotal != 0 || f.Truncated {
		t.Errorf("穿透了目标目录: %+v（跟随策略是备份阶段的决定，探查不预支）", f)
	}
}

// 8. 断链 → SymlinkBroken:true，相对目标按链接所在目录解析为绝对。
func TestProbeBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink("missing-relative", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	f := probeOne(t, t.TempDir(), Input{Path: link})
	if f.Kind != KindSymlink {
		t.Fatalf("Kind = %q, want %q", f.Kind, KindSymlink)
	}
	if f.SymlinkTarget != filepath.Join(dir, "missing-relative") {
		t.Errorf("SymlinkTarget = %q, want 相对目标解析为绝对 %q", f.SymlinkTarget, filepath.Join(dir, "missing-relative"))
	}
	if !f.SymlinkBroken {
		t.Error("SymlinkBroken = false, want true")
	}
}

// 9. ~ 展开：Prober{home} 探 ~/foo → 命中 home 下的文件，Path 原样回显。
func TestProbeTildeExpansion(t *testing.T) {
	home := t.TempDir()
	writeFile(t, home, "foo", "x")

	f := probeOne(t, home, Input{Path: "~/foo"})
	if !f.Exists || f.Kind != KindFile {
		t.Fatalf("Facts = %+v, want file under injected home", f)
	}
	if f.Path != "~/foo" {
		t.Errorf("Path = %q, want 原样回显 %q", f.Path, "~/foo")
	}
}

// 10. ~/.ssh/id_rsa → SensitiveFloor:true。
func TestProbeSensitiveFloorHit(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, ssh, "id_rsa", "x") // 内容无所谓——本包永不读

	f := probeOne(t, home, Input{Path: "~/.ssh/id_rsa"})
	if !f.Exists {
		t.Fatal("Exists = false, fixture error")
	}
	if !f.SensitiveFloor {
		t.Error("SensitiveFloor = false, want true（命中 ~/.ssh/id_* 底线）")
	}
}

// 11. 目录里放 secrets.pem → 进 SensitiveEntries，父条目自身不标记。
func TestProbeSensitiveEntriesInSample(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir() // 普通目录，不命中任何底线
	writeFile(t, dir, "secrets.pem", "x")
	writeFile(t, dir, "normal.txt", "x")

	f := probeOne(t, home, Input{Path: dir})
	if f.SensitiveFloor {
		t.Error("SensitiveFloor = true, want false（父目录自身不命中）")
	}
	if len(f.SensitiveEntries) != 1 || f.SensitiveEntries[0] != "secrets.pem" {
		t.Errorf("SensitiveEntries = %v, want [secrets.pem]（只是名字，不是内容）", f.SensitiveEntries)
	}
	if len(f.EntriesSample) != 2 {
		t.Errorf("len(EntriesSample) = %d, want 2", len(f.EntriesSample))
	}
}

// ---- 补丁卡 #12b：底线豁免 .pub + MTime 指针化 ----

// 1. ~/.ssh 放 id_ed25519 + id_ed25519.pub → SensitiveEntries 恰好只有
// 私钥；公钥仍在采样里（豁免不等于隐身），但无任何标记。
func TestFloorExcludesPubKeys(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, ssh, "id_ed25519", "x")
	writeFile(t, ssh, "id_ed25519.pub", "x")

	f := probeOne(t, home, Input{Path: "~/.ssh"})
	if len(f.SensitiveEntries) != 1 || f.SensitiveEntries[0] != "id_ed25519" {
		t.Errorf("SensitiveEntries = %v, want [id_ed25519]（.pub 豁免）", f.SensitiveEntries)
	}
	seen := false
	for _, n := range f.EntriesSample {
		if n == "id_ed25519.pub" {
			seen = true
		}
	}
	if !seen {
		t.Error("id_ed25519.pub missing from EntriesSample（豁免不等于隐身）")
	}
}

// 2. 直接探公钥 → SensitiveFloor:false（豁免对顶层路径同样生效）。
func TestFloorPubKeyNotMarked(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, ssh, "id_ed25519.pub", "x")

	f := probeOne(t, home, Input{Path: "~/.ssh/id_ed25519.pub"})
	if f.SensitiveFloor {
		t.Error("SensitiveFloor = true, want false（公钥不是秘密）")
	}
}

// 3. 豁免没有扩大化：不带 .pub 的私钥照拦。
func TestFloorStillBlocksPrivateKeys(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, ssh, "id_rsa", "x")

	f := probeOne(t, home, Input{Path: "~/.ssh/id_rsa"})
	if !f.SensitiveFloor {
		t.Error("SensitiveFloor = false, want true（豁免只放行 .pub）")
	}
}

// 4. MTime 指针化：文件 → 非 nil 且正确；不存在 → nil
// （不存在/失败的条目不带 "0001-01-01" 的零时间噪音）。
func TestFactsMTimePointer(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "timed.txt", "x")
	before := time.Now().Add(-time.Second)

	f := probeOne(t, dir, Input{Path: path})
	if f.MTime == nil {
		t.Fatal("MTime = nil, want non-nil for existing file")
	}
	if f.MTime.Before(before) || f.MTime.After(time.Now().Add(time.Second)) {
		t.Errorf("MTime = %v, want around now", *f.MTime)
	}

	missing := probeOne(t, dir, Input{Path: filepath.Join(dir, "nope")})
	if missing.MTime != nil {
		t.Errorf("MTime = %v, want nil（不存在条目整个字段不出现）", missing.MTime)
	}
}

// 12. 无权限：父目录 chmod 000 → 该条 Error 非空、其余条目完好。
func TestProbePermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受权限位约束")
	}
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.MkdirAll(filepath.Join(parent, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 先注册恢复，再收紧：TempDir 的清理（删除）才走得动。
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}

	home := t.TempDir()
	good := writeFile(t, home, "ok.txt", "x")

	out := New(home).Probe(context.Background(), []Input{
		{Path: filepath.Join(parent, "sub")}, // 穿不过无 x 的父目录
		{Path: parent},                       // 门牌可见，进不了门
		{Path: good},                         // 同批的健康条目
	})
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}

	if out[0].Exists || out[0].Error == "" {
		t.Errorf("sub Facts = %+v, want Error 非空 / Exists false（Lstat 就失败）", out[0])
	}
	if !out[1].Exists || out[1].Kind != KindDir || out[1].Error == "" {
		t.Errorf("parent Facts = %+v, want 可见 dir 但 ReadDir 失败带 Error", out[1])
	}
	if !out[2].Exists || out[2].Error != "" {
		t.Errorf("good Facts = %+v, want healthy（单条失败不拖垮整批）", out[2])
	}
}

// 13. 相对路径 → 该条 Error（防御性自保；信封 rejected 是卡 #13 的事）。
// ~user 形态同样拒绝——那是别人的家，探查者无权假设其位置。
func TestProbeRelativePathRejected(t *testing.T) {
	home := t.TempDir()
	for _, bad := range []string{"foo/bar", "./x", "~otheruser/secret"} {
		f := probeOne(t, home, Input{Path: bad})
		if f.Error == "" {
			t.Errorf("path %q: Error = empty, want rejection", bad)
		}
		if f.Exists {
			t.Errorf("path %q: Exists = true, want false", bad)
		}
	}
}

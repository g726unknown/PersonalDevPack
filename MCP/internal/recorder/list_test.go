package recorder

import (
	"testing"
	"time"
)

// 本层测试全部纯内存：不需要 Open、不需要临时目录。

var (
	upT0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	upT1 = upT0.Add(time.Hour)
	upT2 = upT0.Add(2 * time.Hour)
)

func newList() *List {
	return &List{Version: CurrentVersion, Packages: map[string]*Package{}}
}

func TestUpsertNewPackageIsAdded(t *testing.T) {
	l := newList()

	act, p, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "0.11.2", Source: "pacman"}, upT0)
	if err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	if act != ActionAdded {
		t.Errorf("Action = %q, want %q", act, ActionAdded)
	}
	if p == nil {
		t.Fatal("returned Package = nil")
	}
	if p.Name != "neovim" || p.Source != "pacman" || p.Version != "0.11.2" || p.Type != TypeDevTool {
		t.Errorf("package = %+v", *p)
	}
	if !p.RecordedAt.Equal(upT0) {
		t.Errorf("RecordedAt = %v, want %v", p.RecordedAt, upT0)
	}
	if !p.CheckedAt.Equal(upT0) {
		t.Errorf("CheckedAt = %v, want %v", p.CheckedAt, upT0)
	}
	if p.RemovedAt != nil {
		t.Errorf("RemovedAt = %v, want nil", p.RemovedAt)
	}
	// 返回的指针必须是 map 里的真身，不是副本。
	if l.Packages["neovim"] != p {
		t.Error("returned pointer is not the stored entry")
	}
}

func TestUpsertIdenticalFactsIsUnchanged(t *testing.T) {
	l := newList()
	f := Facts{Type: TypeOther, Version: "3.4.0", Source: "pacman"}
	if _, _, err := l.Upsert("htop", f, upT0); err != nil {
		t.Fatalf("first Upsert() error = %v, err", err)
	}

	act, p, err := l.Upsert("htop", f, upT1)
	if err != nil {
		t.Fatalf("second Upsert() error = %v, want nil", err)
	}
	if act != ActionUnchanged {
		t.Errorf("Action = %q, want %q", act, ActionUnchanged)
	}
	if !p.RecordedAt.Equal(upT0) {
		t.Errorf("RecordedAt = %v, want untouched %v", p.RecordedAt, upT0)
	}
	if !p.CheckedAt.Equal(upT0) {
		t.Errorf("CheckedAt = %v, want untouched %v", p.CheckedAt, upT0)
	}
	if p.Type != f.Type || p.Version != f.Version || p.Source != f.Source {
		t.Errorf("package = %+v, want fields untouched", *p)
	}
}

func TestUpsertChangedVersionIsUpdated(t *testing.T) {
	l := newList()
	if _, _, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "0.11.2", Source: "pacman"}, upT0); err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}

	act, p, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "0.12.0", Source: "pacman"}, upT1)
	if err != nil {
		t.Fatalf("second Upsert() error = %v, want nil", err)
	}
	if act != ActionUpdated {
		t.Errorf("Action = %q, want %q", act, ActionUpdated)
	}
	if p.Version != "0.12.0" {
		t.Errorf("Version = %q, want %q", p.Version, "0.12.0")
	}
	if !p.CheckedAt.Equal(upT1) {
		t.Errorf("CheckedAt = %v, want %v", p.CheckedAt, upT1)
	}
	if !p.RecordedAt.Equal(upT0) {
		t.Errorf("RecordedAt = %v, want unchanged %v", p.RecordedAt, upT0)
	}
}

func TestUpsertAfterRemovalIsRestored(t *testing.T) {
	l := newList()
	if _, _, err := l.Upsert("htop", Facts{Type: TypeOther, Version: "3.4.0", Source: "aur"}, upT0); err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}
	if act := l.MarkRemoved("htop", upT1); act != ActionRemoved {
		t.Fatalf("MarkRemoved() = %q, want %q", act, ActionRemoved)
	}

	act, p, err := l.Upsert("htop", Facts{Type: TypeOther, Version: "3.6.0", Source: "aur"}, upT2)
	if err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	if act != ActionRestored {
		t.Errorf("Action = %q, want %q", act, ActionRestored)
	}
	if p.RemovedAt != nil {
		t.Errorf("RemovedAt = %v, want nil", p.RemovedAt)
	}
	if p.Version != "3.6.0" {
		t.Errorf("Version = %q, want %q", p.Version, "3.6.0")
	}
	if !p.CheckedAt.Equal(upT2) {
		t.Errorf("CheckedAt = %v, want %v", p.CheckedAt, upT2)
	}
	if !p.RecordedAt.Equal(upT0) {
		t.Errorf("RecordedAt = %v, want first-recorded %v", p.RecordedAt, upT0)
	}
}

func TestUpsertEmptyNameErrorsWithoutSideEffects(t *testing.T) {
	l := newList()
	if _, _, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "1", Source: "pacman"}, upT0); err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}

	before := len(l.Packages)
	act, p, err := l.Upsert("", Facts{Type: TypeOther, Version: "1", Source: "pacman"}, upT1)
	if err == nil {
		t.Fatal("Upsert() error = nil, want error")
	}
	if p != nil {
		t.Errorf("returned Package = %+v, want nil", p)
	}
	if act != "" {
		t.Errorf("Action = %q, want empty on error", act)
	}
	if len(l.Packages) != before {
		t.Errorf("len(Packages) = %d, want unchanged %d", len(l.Packages), before)
	}
}

func TestUpsertInvalidTypeErrorsWithoutSideEffects(t *testing.T) {
	l := newList()
	if _, _, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "0.11.2", Source: "pacman"}, upT0); err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}

	act, p, err := l.Upsert("neovim", Facts{Type: "garbage", Version: "9.9", Source: "aur"}, upT1)
	if err == nil {
		t.Fatal("Upsert() error = nil, want error")
	}
	if p != nil {
		t.Errorf("returned Package = %+v, want nil", p)
	}
	if act != "" {
		t.Errorf("Action = %q, want empty on error", act)
	}
	// 既有条目必须原样：校验发生在改数据之前。
	got := l.Packages["neovim"]
	if got.Type != TypeDevTool || got.Version != "0.11.2" || got.Source != "pacman" {
		t.Errorf("existing entry = %+v, want untouched", *got)
	}
	if !got.CheckedAt.Equal(upT0) {
		t.Errorf("existing CheckedAt = %v, want untouched %v", got.CheckedAt, upT0)
	}
	if len(l.Packages) != 1 {
		t.Errorf("len(Packages) = %d, want 1", len(l.Packages))
	}
}

func TestMarkRemovedThenAgain(t *testing.T) {
	l := newList()
	if _, _, err := l.Upsert("htop", Facts{Type: TypeOther, Version: "3.4.0", Source: "pacman"}, upT0); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if act := l.MarkRemoved("htop", upT1); act != ActionRemoved {
		t.Fatalf("first MarkRemoved() = %q, want %q", act, ActionRemoved)
	}
	p := l.Packages["htop"]
	if p.RemovedAt == nil || !p.RemovedAt.Equal(upT1) {
		t.Fatalf("RemovedAt = %v, want %v", p.RemovedAt, upT1)
	}

	// 再删一次：already_removed，RemovedAt 保持第一次的值。
	if act := l.MarkRemoved("htop", upT2); act != ActionAlreadyRemoved {
		t.Fatalf("second MarkRemoved() = %q, want %q", act, ActionAlreadyRemoved)
	}
	if !p.RemovedAt.Equal(upT1) {
		t.Errorf("RemovedAt = %v, want untouched %v", p.RemovedAt, upT1)
	}
	// 软删除不物理删除：条目仍在 map 里。
	if _, ok := l.Packages["htop"]; !ok {
		t.Error("entry was physically deleted from the map")
	}
}

func TestMarkRemovedMissingPackageIsNotPresent(t *testing.T) {
	l := newList()

	if act := l.MarkRemoved("ghost", upT0); act != ActionNotPresent {
		t.Errorf("MarkRemoved() = %q, want %q", act, ActionNotPresent)
	}
	if len(l.Packages) != 0 {
		t.Errorf("len(Packages) = %d, want 0", len(l.Packages))
	}
}

func TestUpsertOnNilPackagesMapInitializesIt(t *testing.T) {
	l := &List{Version: CurrentVersion} // Packages 为 nil

	act, _, err := l.Upsert("neovim", Facts{Type: TypeDevTool, Version: "1", Source: "pacman"}, upT0)
	if err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	if act != ActionAdded {
		t.Errorf("Action = %q, want %q", act, ActionAdded)
	}
	if l.Packages == nil {
		t.Fatal("Packages = nil, want initialized by Upsert")
	}
	if len(l.Packages) != 1 {
		t.Errorf("len(Packages) = %d, want 1", len(l.Packages))
	}
}

func TestValidType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{TypeDevTool, true},
		{TypeRuntime, true},
		{TypeService, true},
		{TypeOther, true},
		{"", false},
		{"garbage", false},
	}
	for _, c := range cases {
		if got := ValidType(c.in); got != c.want {
			t.Errorf("ValidType(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

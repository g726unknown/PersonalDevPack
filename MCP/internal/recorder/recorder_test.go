package recorder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mustWrite 在 dir 下预写一份 list.json 并返回其路径。
func mustWrite(t *testing.T, dir string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "list.json")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	return path
}

// wantLoadError 断言 Load 返回了带文件路径的错误。
func wantLoadError(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not mention file path %q", err, path)
	}
}

func TestLoadFileMissingReturnsEmptyList(t *testing.T) {
	l, err := Open(t.TempDir()).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if l.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", l.Version, CurrentVersion)
	}
	if l.Revision != 0 {
		t.Errorf("Revision = %d, want 0", l.Revision)
	}
	if l.Packages == nil {
		t.Fatal("Packages = nil, want non-nil")
	}
	if len(l.Packages) != 0 {
		t.Errorf("len(Packages) = %d, want 0", len(l.Packages))
	}
}

func TestLoadParsesValidFile(t *testing.T) {
	dir := t.TempDir()
	const content = `{
  "version": 1,
  "revision": 7,
  "updated_at": "2026-11-17T09:30:00Z",
  "packages": {
    "neovim": {
      "name": "neovim",
      "source": "pacman",
      "version": "0.11.2",
      "type": "devtool",
      "recorded_at": "2026-11-10T08:00:00Z",
      "checked_at": "2026-11-17T09:30:00Z"
    },
    "htop": {
      "name": "htop",
      "source": "aur",
      "version": "3.4.0",
      "type": "other",
      "recorded_at": "2026-11-12T10:00:00Z",
      "checked_at": "2026-11-15T10:00:00Z",
      "removed_at": "2026-11-18T12:00:00Z"
    }
  }
}`
	mustWrite(t, dir, []byte(content))

	l, err := Open(dir).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if l.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", l.Version, CurrentVersion)
	}
	if l.Revision != 7 {
		t.Errorf("Revision = %d, want 7", l.Revision)
	}
	if !l.UpdatedAt.Equal(time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("UpdatedAt = %v, want 2026-11-17T09:30:00Z", l.UpdatedAt)
	}

	if len(l.Packages) != 2 {
		t.Fatalf("len(Packages) = %d, want 2", len(l.Packages))
	}

	nv := l.Packages["neovim"]
	if nv == nil {
		t.Fatal("Packages[\"neovim\"] missing")
	}
	if nv.Name != "neovim" || nv.Source != "pacman" || nv.Version != "0.11.2" || nv.Type != "devtool" {
		t.Errorf("neovim = %+v", *nv)
	}
	if !nv.RecordedAt.Equal(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("neovim.RecordedAt = %v, want 2026-11-10T08:00:00Z", nv.RecordedAt)
	}
	if !nv.CheckedAt.Equal(time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("neovim.CheckedAt = %v, want 2026-11-17T09:30:00Z", nv.CheckedAt)
	}
	if nv.RemovedAt != nil {
		t.Errorf("neovim.RemovedAt = %v, want nil", nv.RemovedAt)
	}

	ht := l.Packages["htop"]
	if ht == nil {
		t.Fatal("Packages[\"htop\"] missing")
	}
	if ht.Source != "aur" || ht.Type != "other" {
		t.Errorf("htop = %+v", *ht)
	}
	if ht.RemovedAt == nil {
		t.Fatal("htop.RemovedAt = nil, want soft-removal timestamp")
	}
	if !ht.RemovedAt.Equal(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("htop.RemovedAt = %v, want 2026-11-18T12:00:00Z", *ht.RemovedAt)
	}
}

func TestLoadValidFileWithoutPackagesKey(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, []byte(`{"version":1,"revision":3,"updated_at":"2026-11-17T09:30:00Z"}`))

	l, err := Open(dir).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if l.Packages == nil {
		t.Fatal("Packages = nil, want non-nil")
	}
	if len(l.Packages) != 0 {
		t.Errorf("len(Packages) = %d, want 0", len(l.Packages))
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	cases := map[string][]byte{
		"zero bytes":     nil,
		"truncated JSON": []byte(`{"坏`),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := mustWrite(t, dir, content)

			l, err := Open(dir).Load(context.Background())
			if l != nil {
				t.Errorf("Load() list = %+v, want nil", l)
			}
			wantLoadError(t, err, path)
		})
	}
}

func TestLoadUnsupportedVersion(t *testing.T) {
	cases := map[string]string{
		"version 2":       `{"version":2,"revision":0,"updated_at":"2026-01-01T00:00:00Z","packages":{}}`,
		"missing version": `{"revision":0,"updated_at":"2026-01-01T00:00:00Z","packages":{}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := mustWrite(t, dir, []byte(content))

			l, err := Open(dir).Load(context.Background())
			if l != nil {
				t.Errorf("Load() list = %+v, want nil", l)
			}
			wantLoadError(t, err, path)
		})
	}
}

func TestLoadReadErrorOtherThanNotExist(t *testing.T) {
	// list.json 是一个目录：open 成功、read 失败，
	// 这类错误必须如实上报，而不是被当成空清单。
	dir := t.TempDir()
	path := filepath.Join(dir, "list.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	l, err := Open(dir).Load(context.Background())
	if l != nil {
		t.Errorf("Load() list = %+v, want nil", l)
	}
	wantLoadError(t, err, path)
}

// ---- Update（任务卡 #2）----

// addPkg 返回一个往清单里加包的 fn。
func addPkg(name string) func(*List) error {
	return func(l *List) error {
		l.Packages[name] = &Package{
			Name:       name,
			Source:     "pacman",
			Type:       "other",
			RecordedAt: time.Now(),
			CheckedAt:  time.Now(),
		}
		return nil
	}
}

func TestUpdateOnEmptyDirCreatesList(t *testing.T) {
	r := Open(t.TempDir())

	got, err := r.Update(context.Background(), 0, addPkg("neovim"))
	if err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}
	if got.Revision != 1 {
		t.Errorf("returned Revision = %d, want 1", got.Revision)
	}
	if _, ok := got.Packages["neovim"]; !ok {
		t.Error("returned list does not contain neovim")
	}

	// 重新从盘上读，确认真的落盘了。
	l, err := r.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if l.Revision != 1 {
		t.Errorf("on-disk Revision = %d, want 1", l.Revision)
	}
	if _, ok := l.Packages["neovim"]; !ok {
		t.Error("on-disk list does not contain neovim")
	}
}

func TestUpdateSequentialWritesIncrementRevision(t *testing.T) {
	r := Open(t.TempDir())
	ctx := context.Background()

	for wantRev := 1; wantRev <= 2; wantRev++ {
		got, err := r.Update(ctx, wantRev-1, addPkg(fmt.Sprintf("pkg-%d", wantRev)))
		if err != nil {
			t.Fatalf("Update(baseRev=%d) error = %v, want nil", wantRev-1, err)
		}
		if got.Revision != wantRev {
			t.Errorf("Revision = %d, want %d", got.Revision, wantRev)
		}
	}

	l, err := r.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if l.Revision != 2 {
		t.Errorf("on-disk Revision = %d, want 2", l.Revision)
	}
	if len(l.Packages) != 2 {
		t.Errorf("len(Packages) = %d, want 2", len(l.Packages))
	}
}

func TestUpdateStaleBaseRevReturnsConflict(t *testing.T) {
	r := Open(t.TempDir())
	ctx := context.Background()

	if _, err := r.Update(ctx, 0, addPkg("neovim")); err != nil {
		t.Fatalf("first Update() error = %v, want nil", err)
	}
	before, err := r.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	// baseRev=0 已过期：盘上此刻是 1。
	_, err = r.Update(ctx, 0, addPkg("htop"))
	if err == nil {
		t.Fatal("Update() error = nil, want conflict")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("errors.Is(err, ErrConflict) = false, err = %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As(err, *ConflictError) = false, err = %v", err)
	}
	if ce.Current != 1 {
		t.Errorf("ConflictError.Current = %d, want 1", ce.Current)
	}

	// 盘上内容必须没被这次失败的调用改变。
	after, err := r.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if after.Revision != before.Revision {
		t.Errorf("on-disk Revision = %d, want unchanged %d", after.Revision, before.Revision)
	}
	if _, ok := after.Packages["htop"]; ok {
		t.Error("htop was written despite conflict")
	}
}

func TestUpdateFnErrorWritesNothing(t *testing.T) {
	dir := t.TempDir()
	r := Open(dir)

	fnErr := errors.New("fn aborted")
	if _, err := r.Update(context.Background(), 0, func(*List) error { return fnErr }); !errors.Is(err, fnErr) {
		t.Fatalf("Update() error = %v, want fn's own error", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		// .list.lock 存在是正常的；不允许出现清单文件和临时文件。
		if e.Name() == "list.json" {
			t.Error("list.json exists, want nothing written")
		}
		if strings.HasPrefix(e.Name(), ".list-") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s leaked", e.Name())
		}
	}
}

func TestUpdateConcurrentSameBaseRevOnlyOneWins(t *testing.T) {
	r := Open(t.TempDir())
	ctx := context.Background()

	const n = 8
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Update(ctx, 0, addPkg(fmt.Sprintf("pkg-%d", i)))
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var okCount, conflictCount int
	for err := range results {
		switch {
		case err == nil:
			okCount++
		case errors.Is(err, ErrConflict):
			conflictCount++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if okCount != 1 {
		t.Errorf("successes = %d, want 1", okCount)
	}
	if conflictCount != n-1 {
		t.Errorf("conflicts = %d, want %d", conflictCount, n-1)
	}

	l, err := r.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if l.Revision != 1 {
		t.Errorf("final Revision = %d, want 1", l.Revision)
	}
	if len(l.Packages) != 1 {
		t.Errorf("len(Packages) = %d, want 1", len(l.Packages))
	}
}

func TestUpdateFileModeIs0600(t *testing.T) {
	dir := t.TempDir()
	r := Open(dir)

	if _, err := r.Update(context.Background(), 0, addPkg("neovim")); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "list.json"))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
}

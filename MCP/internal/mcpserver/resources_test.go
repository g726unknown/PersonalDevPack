package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/recorder"
)

func TestListResources(t *testing.T) {
	cs, _, _ := newSession(t)

	res, err := cs.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	want := map[string]bool{
		"pdp://state/inventory": false,
		"pdp://system/profile":  false,
		"pdp://guide":           false,
	}
	if len(res.Resources) != len(want) {
		t.Fatalf("len(Resources) = %d, want %d", len(res.Resources), len(want))
	}
	for _, r := range res.Resources {
		seen, ok := want[r.URI]
		if !ok {
			t.Errorf("unexpected resource %q", r.URI)
			continue
		}
		if seen {
			t.Errorf("duplicate resource %q", r.URI)
		}
		want[r.URI] = true
	}
	for uri, seen := range want {
		if !seen {
			t.Errorf("resource %q missing", uri)
		}
	}
}

func TestReadInventoryIncludesSoftRemoved(t *testing.T) {
	cs, rec, _ := newSession(t)

	seed(t, rec, "neovim", recorder.TypeDevTool)
	seed(t, rec, "ghost", recorder.TypeOther)
	l, err := rec.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := rec.Update(t.Context(), l.Revision, func(li *recorder.List) error {
		li.MarkRemoved("ghost", showSeedTime)
		return nil
	}); err != nil {
		t.Fatalf("remove ghost: %v", err)
	}

	res, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://state/inventory"})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("len(Contents) = %d, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.MIMEType != "application/json" {
		t.Errorf("MIMEType = %q, want application/json", c.MIMEType)
	}

	// 全量：软删条目在场——这正是本资源与 pdp_list_show 并存的差异
	//（show 故意丢掉软删；资源注释互指）。
	var full recorder.List
	if err := json.Unmarshal([]byte(c.Text), &full); err != nil {
		t.Fatalf("unmarshal inventory: %v", err)
	}
	if full.Packages["ghost"] == nil {
		t.Fatal("soft-removed ghost missing from inventory (it is this resource's reason to exist)")
	}
	if full.Packages["ghost"].RemovedAt == nil {
		t.Error("ghost.RemovedAt = nil, want soft-removal mark")
	}
	if full.Packages["neovim"] == nil {
		t.Error("normal entry missing from inventory")
	}
}

func TestReadProfile(t *testing.T) {
	st := sysStub("6.12.1-arch1-1", "x86_64", true)
	cs, _, dir := newSessionWith(t, st)

	res, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://system/profile"})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("len(Contents) = %d, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.MIMEType != "application/json" {
		t.Errorf("MIMEType = %q, want application/json", c.MIMEType)
	}

	var p map[string]any
	if err := json.Unmarshal([]byte(c.Text), &p); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}
	os, ok := p["os"].(map[string]any)
	if !ok {
		t.Fatalf("profile.os 类型 = %T, want object", p["os"])
	}
	if os["id"] != "arch" {
		t.Errorf("os.id = %v, want arch (真机 os-release)", os["id"])
	}
	paths, ok := p["paths"].(map[string]any)
	if !ok {
		t.Fatalf("profile.paths 类型 = %T", p["paths"])
	}
	if paths["state_dir"] != dir {
		t.Errorf("paths.state_dir = %v, want cfg 注入的 %q", paths["state_dir"], dir)
	}
}

func TestReadGuide(t *testing.T) {
	cs, _, _ := newSession(t)

	res, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://guide"})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("len(Contents) = %d, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.MIMEType != "text/markdown" {
		t.Errorf("MIMEType = %q, want text/markdown", c.MIMEType)
	}
	if strings.TrimSpace(c.Text) == "" {
		t.Error("guide text = empty, want non-empty")
	}
	if !strings.Contains(c.Text, "pdp_list_add") {
		t.Error("guide text 缺少 pdp_list_add 字样（标准流程段应提到）")
	}
	// #14b：自行/询问分界节的关键字级断言（不钉全句，措辞可打磨）。
	if !strings.Contains(c.Text, "自行决断") {
		t.Error("guide text 缺少 自行决断 字样（分界节应在场）")
	}
	if !strings.Contains(c.Text, "先问再存档") {
		t.Error("guide text 缺少 先问再存档 字样（隐私触发器应在场）")
	}
}

func TestToolsUnchangedByResourceRegistration(t *testing.T) {
	// 注册重构后工具不丢：resources 挂上不影响 tools/list。
	cs, _, _ := newSession(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(res.Tools) != 8 {
		t.Errorf("len(Tools) = %d, want 8 (资源注册不得丢工具)", len(res.Tools))
	}
}

func TestResourceReadErrorPassesThrough(t *testing.T) {
	cs, _, dir := newSession(t)

	if err := os.WriteFile(filepath.Join(dir, "list.json"), []byte(`{"坏`), 0o644); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}

	if _, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "pdp://state/inventory"}); err == nil {
		t.Fatal("ReadResource() error = nil, want passthrough error (corrupt list.json)")
	}
}

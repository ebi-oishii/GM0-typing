package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileAgents(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("複数エージェント形式", func(t *testing.T) {
		path := write("multi.json", `{"agents":[{"name":"claude","status":"working","task":"tests"},{"name":"codex","status":"blocked"}]}`)
		agents := fileAgents(path)
		if len(agents) != 2 {
			t.Fatalf("agents = %d, want 2", len(agents))
		}
		if agents[0].Name != "claude" || agents[0].Status != "working" || agents[0].Note != "tests" {
			t.Errorf("unexpected agent[0]: %+v", agents[0])
		}
		if !agents[1].NeedsAttention() {
			t.Error("blocked agent should need attention")
		}
	})

	t.Run("単一オブジェクト形式", func(t *testing.T) {
		path := write("single.json", `{"name":"claude","status":"done"}`)
		agents := fileAgents(path)
		if len(agents) != 1 || agents[0].Status != "done" || !agents[0].NeedsAttention() {
			t.Fatalf("unexpected agents: %+v", agents)
		}
	})

	t.Run("壊れたJSONは無視", func(t *testing.T) {
		path := write("broken.json", `{oops`)
		if agents := fileAgents(path); agents != nil {
			t.Fatalf("agents = %+v, want nil", agents)
		}
	})

	t.Run("存在しないファイルは無視", func(t *testing.T) {
		if agents := fileAgents(filepath.Join(dir, "nope.json")); agents != nil {
			t.Fatalf("agents = %+v, want nil", agents)
		}
	})
}

func TestParseHerdrPanes(t *testing.T) {
	payload := []byte(`{"id":"cli:pane:list","result":{"type":"pane_list","panes":[
		{"pane_id":"w1:p1","agent_status":"unknown","focused":false},
		{"pane_id":"w1:p2","agent":"claude","agent_status":"working","focused":true},
		{"pane_id":"w1:p3","agent":"codex","agent_status":"blocked","focused":false,"custom_status":"perm wait"},
		{"pane_id":"w1:p9","agent":"claude","agent_status":"working","focused":false}
	]}}`)

	t.Run("エージェント抽出とフォーカス検出", func(t *testing.T) {
		agents, focusPane, focusStatus := parseHerdrPanes(payload, "w1:p9")
		if len(agents) != 2 {
			t.Fatalf("agents = %d, want 2 (自ペイン w1:p9 と非エージェントは除外)", len(agents))
		}
		if focusPane != "w1:p2" || focusStatus != "working" {
			t.Errorf("focus = (%q, %q), want (w1:p2, working)", focusPane, focusStatus)
		}
		if agents[1].Note != "perm wait" {
			t.Errorf("custom_status が Note に入るはず: %+v", agents[1])
		}
	})

	t.Run("非エージェントペインのフォーカスは保護対象外", func(t *testing.T) {
		noAgentFocus := []byte(`{"result":{"panes":[
			{"pane_id":"w1:p1","agent_status":"unknown","focused":true},
			{"pane_id":"w1:p2","agent":"claude","agent_status":"working","focused":false}
		]}}`)
		_, focusPane, _ := parseHerdrPanes(noAgentFocus, "")
		if focusPane != "" {
			t.Errorf("ゲーム側ペインにフォーカスがあるだけなら focusPane は空のはず: %q", focusPane)
		}
	})

	t.Run("自ペインがフォーカスされていても保護しない", func(t *testing.T) {
		ownFocus := []byte(`{"result":{"panes":[
			{"pane_id":"w1:p9","agent":"claude","agent_status":"working","focused":true}
		]}}`)
		agents, focusPane, _ := parseHerdrPanes(ownFocus, "w1:p9")
		if len(agents) != 0 || focusPane != "" {
			t.Errorf("自ペインは除外: agents=%+v focusPane=%q", agents, focusPane)
		}
	})

	t.Run("壊れたJSON", func(t *testing.T) {
		agents, focusPane, _ := parseHerdrPanes([]byte("not json"), "")
		if agents != nil || focusPane != "" {
			t.Error("壊れたJSONは (nil, \"\", \"\") を返すはず")
		}
	})
}

func TestTailHash(t *testing.T) {
	// 実際の Claude Code の working 中画面を模したレイアウト:
	// 出力 → スピナー行 → 枠線 → 入力欄 ❯ → 枠線 → ステータスバー
	screen := func(output, spinner, composer, statusBar string) []byte {
		return []byte(output + "\n" + spinner + "\n────\n" + composer + "\n────\n" + statusBar + "\n")
	}
	base, ok := tailHash(screen("output line", "· Manifesting… (10s · 1.2k tokens)", "❯", "⏸ manual mode on"))
	if !ok {
		t.Fatal("入力欄 ❯ を特定できるはず")
	}

	t.Run("生成中の出力・スピナー・ステータスバーの変化は無視", func(t *testing.T) {
		h, _ := tailHash(screen("MORE output streaming", "· Manifesting… (12s · 3.4k tokens)", "❯", "⏸ manual mode on · ← 2 agents"))
		if h != base {
			t.Error("入力欄より上の変化とステータスバーの回転でハッシュが変わってはいけない")
		}
	})

	t.Run("入力欄への打ち込みは検知", func(t *testing.T) {
		h, _ := tailHash(screen("output line", "· Manifesting… (10s · 1.2k tokens)", "❯ fix the tests", "⏸ manual mode on"))
		if h == base {
			t.Error("入力欄の変化はハッシュに反映されるはず")
		}
	})

	t.Run("複数行プロンプト（改行・折り返し）も検知", func(t *testing.T) {
		multiline := func(lastLine string) []byte {
			s := "output line\n· Manifesting… (10s)\n────\n❯ 長いプロンプトの1行目\n"
			for i := 0; i < 12; i++ {
				s += "  継続行がたくさん続く\n"
			}
			return []byte(s + lastLine + "\n────\n⏸ status bar\n")
		}
		a, okA := tailHash(multiline("  ここまで打った"))
		b, okB := tailHash(multiline("  ここまで打ったぞ"))
		if !okA || !okB {
			t.Fatal("❯ が末尾から十数行上にあっても入力欄を特定できるはず")
		}
		if a == b {
			t.Error("継続行への打ち込みも検知されるはず")
		}
	})

	t.Run("会話ログ中の ❯ は入力欄と誤認しない", func(t *testing.T) {
		// ❯ が探索範囲（末尾25行）より上にしかない画面
		lines := "❯ old user message\n"
		for i := 0; i < 27; i++ {
			lines += "output\n"
		}
		_, ok := tailHash([]byte(lines + "status bar\n"))
		if ok {
			t.Error("画面下部に ❯ がなければ composer = false のはず")
		}
	})

	t.Run("入力欄が見つからなければ画面全体で判定", func(t *testing.T) {
		a, okA := tailHash([]byte("some output\nno prompt here\nstatus\n"))
		b, okB := tailHash([]byte("DIFFERENT output\nno prompt here\nstatus\n"))
		if okA || okB {
			t.Fatal("composer = false のはず")
		}
		if a == b {
			t.Error("フォールバック時は画面全体の変化を検知するはず")
		}
	})
}

func TestShouldSample(t *testing.T) {
	cases := []struct {
		composer bool
		status   string
		want     bool
	}{
		{true, "working", true},   // 入力欄を分離できるなら生成中でも採取
		{false, "working", false}, // 分離できないなら生成中は誤検知するので採取しない
		{false, "idle", true},
		{false, "blocked", true},
	}
	for _, c := range cases {
		if got := shouldSample(c.composer, c.status); got != c.want {
			t.Errorf("shouldSample(%v, %q) = %v, want %v", c.composer, c.status, got, c.want)
		}
	}
}

func TestNextRank(t *testing.T) {
	cases := []struct {
		reported int
		remain   int
		name     string
	}{
		{0, 2000, "B"},
		{1999, 1, "B"},
		{2000, 3000, "A"},
		{9999, 1, "S"},
		{24999, 1, "SS"},
		{25000, 0, ""},
	}
	for _, c := range cases {
		remain, name := nextRank(c.reported)
		if remain != c.remain || name != c.name {
			t.Errorf("nextRank(%d) = (%d, %q), want (%d, %q)", c.reported, remain, name, c.remain, c.name)
		}
	}
}

func TestHumanInt(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1010: "1,010", 1234567: "1,234,567"}
	for n, want := range cases {
		if got := humanInt(n); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", n, got, want)
		}
	}
}

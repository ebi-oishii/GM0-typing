package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func applyPoll(t *testing.T, m model, p aiPoll) model {
	t.Helper()
	next, _ := m.Update(pollMsg(p))
	return next.(model)
}

func TestAITypingDetection(t *testing.T) {
	m := newModel(Config{})

	// 1回目: 基準ハッシュの採取だけ。まだ入力中とは判定しない
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p2", FocusTailHash: 111})
	if m.aiTyping {
		t.Error("初回ポーリングでは aiTyping = false のはず")
	}

	// 2回目: 同じペインで入力欄領域が変化 → 指示入力中
	// （生成中の出力との分離は採取側 shouldSample/tailHash が担保する）
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p2", FocusTailHash: 222})
	if !m.aiTyping {
		t.Error("エージェントペインの入力欄が変化したら aiTyping = true のはず")
	}

	// 3回目: 変化なし → 入力停止
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p2", FocusTailHash: 222})
	if m.aiTyping {
		t.Error("変化しなければ aiTyping = false のはず")
	}

	// 採取できなかったポーリング（hash 0）は判定に使わない
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p2", FocusTailHash: 0})
	if m.aiTyping {
		t.Error("hash 0 のポーリングでは aiTyping = false のはず")
	}

	// フォーカスが移った直後は基準がないので判定しない
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p5", FocusTailHash: 444})
	if m.aiTyping {
		t.Error("別ペインへのフォーカス直後は aiTyping = false のはず")
	}
}

func TestFocusAloneDoesNotProtect(t *testing.T) {
	m := newModel(Config{})
	m.eval = 50
	m.lastKeyAt = time.Now().Add(-time.Hour) // ゲーム内でも打鍵していない

	// AIペインにフォーカスしているが、画面は変化していない（= タイピングしていない）
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p2", FocusTailHash: 111})
	m = applyPoll(t, m, aiPoll{FocusPaneID: "w1:p2", FocusTailHash: 111})

	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(model)
	if m.eval >= 50 {
		t.Errorf("フォーカスだけでは保護されず減衰するはず: eval = %v", m.eval)
	}
}

func TestGameOver(t *testing.T) {
	m := newModel(Config{})
	m.eval = 0.1
	m.alert = true // AIを待たせて放置している

	// 減衰で0に到達 → 出社回帰
	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(model)
	if !m.gameOver || m.recalls != 1 {
		t.Fatalf("評価0で出社回帰のはず: gameOver=%v recalls=%d", m.gameOver, m.recalls)
	}

	// 出社中の打鍵・休憩は受理されない
	before := m.taps
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.taps != before || m.paused {
		t.Error("出社中は打鍵も休憩もできないはず")
	}

	// さらに減衰しても2回目の出社回帰にはならない
	next, _ = m.Update(tickMsg(time.Now()))
	m = next.(model)
	if m.recalls != 1 {
		t.Errorf("出社中に出社回帰が重複カウントされている: %d", m.recalls)
	}

	// enter で再出社
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.gameOver || m.eval != evalStart {
		t.Errorf("enter で再出社して eval=%v に戻るはず: gameOver=%v eval=%v", evalStart, m.gameOver, m.eval)
	}
}

func TestAIIdleLock(t *testing.T) {
	m := newModel(Config{})

	// AIはいるが誰も働いていない → ロック
	m = applyPoll(t, m, aiPoll{Agents: []Agent{{Name: "claude", Status: "idle"}}})
	if !m.aiIdle || m.multiplier != 0 {
		t.Fatalf("全員 idle ならロックのはず: aiIdle=%v mult=%d", m.aiIdle, m.multiplier)
	}
	before := m.taps
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(model)
	if m.taps != before {
		t.Error("AI待機中の打鍵はカウントされないはず")
	}

	// 仕事を振って working になれば再開（倍率も復活）
	m = applyPoll(t, m, aiPoll{Agents: []Agent{{Name: "claude", Status: "working"}}})
	if m.aiIdle || m.multiplier != 2 {
		t.Errorf("working がいれば再開して x2 のはず: aiIdle=%v mult=%d", m.aiIdle, m.multiplier)
	}

	// blocked が混ざれば idle ロックではなく alert が優先
	m = applyPoll(t, m, aiPoll{Agents: []Agent{
		{Name: "claude", Status: "idle"},
		{Name: "codex", Status: "blocked"},
	}})
	if m.aiIdle || !m.alert {
		t.Errorf("blocked は alert 扱いのはず: aiIdle=%v alert=%v", m.aiIdle, m.alert)
	}

	// 監視対象が1体もいなければソロ稼働モードで遊べる
	m = applyPoll(t, m, aiPoll{})
	if m.aiIdle || m.alert || m.multiplier != 1 {
		t.Errorf("AIなしはソロ x1 のはず: aiIdle=%v alert=%v mult=%d", m.aiIdle, m.alert, m.multiplier)
	}
}

func TestBreakMode(t *testing.T) {
	m := newModel(Config{})

	// esc で休憩に入る
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !m.paused {
		t.Fatal("esc で休憩に入るはず")
	}

	// 休憩中の打鍵は受理されない
	before := m.taps
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(model)
	if m.taps != before {
		t.Error("休憩中の打鍵はカウントされないはず")
	}

	// 休憩中は評価メーターが凍結される
	m.eval = 50
	m.lastKeyAt = time.Now().Add(-time.Hour) // 放置状態でも
	m.alert = true                           // AIが待っていても
	next, _ = m.Update(tickMsg(time.Now()))
	m = next.(model)
	if m.eval != 50 {
		t.Errorf("休憩中は eval が変化しないはず: %v", m.eval)
	}

	// esc で業務再開
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.paused {
		t.Error("esc で休憩から復帰するはず")
	}
}

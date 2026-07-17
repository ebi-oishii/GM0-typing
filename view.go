package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	cGreen  = lipgloss.Color("42")
	cRed    = lipgloss.Color("196")
	cYellow = lipgloss.Color("220")
	cCyan   = lipgloss.Color("45")
	cDim    = lipgloss.Color("242")
	cWhite  = lipgloss.Color("255")

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cWhite)
	sSub    = lipgloss.NewStyle().Foreground(cDim)
	sBig    = lipgloss.NewStyle().Bold(true).Foreground(cYellow)
	sGood   = lipgloss.NewStyle().Foreground(cGreen)
	sBad    = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	sWarn   = lipgloss.NewStyle().Foreground(cYellow)
	sTyped  = lipgloss.NewStyle().Foreground(cGreen)
	sCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cCyan).Bold(true)
	sWrong  = lipgloss.NewStyle().Foreground(cWhite).Background(cRed).Bold(true)
	sRest   = lipgloss.NewStyle().Foreground(cDim)

	sPhraseBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cCyan).
			Padding(0, 2)
	sAlertBox = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(cRed).
			Foreground(cWhite).
			Padding(0, 2)
	sBreakBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cYellow).
			Padding(0, 2)
)

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Minute {
		return fmt.Sprintf("%d分%02d秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d秒", int(d.Seconds()))
}

func (m model) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	inner := min(w-2, 96)

	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }

	// タイトル
	title := sTitle.Render(" PECKR ") + sSub.Render("─ 評価防衛型タイピング ─ あなたの評価はタイピング数で決まる（らしい）")
	line(title)
	line("")

	// KPI: 申告用入力数と倍率
	multStr := ""
	switch {
	case m.alert:
		multStr = sBad.Render("倍率 x0（サボり判定中）")
	case m.aiIdle:
		multStr = sWarn.Render("倍率 x0（AI待機中）")
	case m.multiplier > 1:
		multStr = sGood.Render(fmt.Sprintf("%s 倍率 x%d（AI %d体が稼働中）",
			spinnerFrames[m.spin], m.multiplier, m.multiplier-1))
	default:
		multStr = sSub.Render("倍率 x1（ソロ稼働）")
	}
	switch {
	case m.gameOver:
		multStr += sBad.Render(" ・ 🏢 出社中")
	case m.paused:
		multStr += sWarn.Render(" ・ ☕ 休憩中")
	case m.aiTyping:
		multStr += sGood.Render(" ・ ⌨ AI指示入力中")
	}
	line(fmt.Sprintf(" 申告用入力数 %s 打  %s",
		sBig.Render(humanInt(m.dayReported())), multStr))
	line(sSub.Render(fmt.Sprintf(" 実測 %s 打 ・ ミス %d ・ 正確率 %.1f%% ・ %d 打/分 ・ コンボ %d（最高 %d）",
		humanInt(m.dayTaps()), m.dayMiss(), m.accuracy(), m.kpmView(), m.combo, m.maxCombo)))
	line("")

	// 評価メーター
	label, color := evalLabel(m.eval)
	rank, rankLabel := rankOf(m.dayReported())
	gaugeW := max(10, inner-26)
	line(fmt.Sprintf(" 評価 %s %3.0f %s",
		gauge(gaugeW, m.eval/evalMax, color),
		m.eval,
		lipgloss.NewStyle().Foreground(color).Render(label)))
	if m.gameOver {
		// ゲームオーバー中は打鍵数に関係なく最低評価
		line(fmt.Sprintf(" 査定 %s ・ %s",
			sBad.Render("F「出社回帰」"), sSub.Render("enter で再出社して挽回せよ")))
	} else {
		remain, nextName := nextRank(m.dayReported())
		nextStr := "王座防衛中"
		if nextName != "" {
			nextStr = fmt.Sprintf("次の %s まで あと %s 打", nextName, humanInt(remain))
		}
		line(fmt.Sprintf(" 査定 %s ・ %s",
			sTitle.Render(rank+"「"+rankLabel+"」"), sSub.Render(nextStr)))
	}
	line("")

	// お題 or 出社回帰 or 休憩 or 警告
	if m.gameOver {
		msg := []string{
			sBad.Render("🏢 出　社　回　帰"),
			"",
			"タイピング量の低下が人事に検知されました。",
			"リモートワーク権限は剥奪されました。",
			"",
			sWarn.Render("enter: 再出社して挽回する ・ ctrl+c: 退勤"),
		}
		line(sAlertBox.Width(inner - 2).Render(strings.Join(msg, "\n")))
	} else if m.paused {
		msg := []string{
			sWarn.Render("☕ 休憩中（" + fmtDuration(time.Since(m.pauseStart)) + "）"),
			sSub.Render("評価メーター凍結・打鍵は受理されません"),
			sSub.Render("esc で業務再開"),
		}
		line(sBreakBox.Width(inner - 2).Render(strings.Join(msg, "\n")))
	} else if m.alert {
		var msg []string
		msg = append(msg, sBad.Render("⚠ 業務連絡: AIがあなたの対応を待っています"))
		for _, a := range m.agents {
			if a.NeedsAttention() {
				msg = append(msg, fmt.Sprintf("  %s %s（%s / %s）",
					statusIcon(a.Status, m.spin), a.Name, a.Status, a.Where))
			}
		}
		if m.aiTyping {
			msg = append(msg, sGood.Render("⌨ 対応の入力を検知（メーター回復中）"))
		} else {
			msg = append(msg, sSub.Render("対応が終わって AI が働き始めると再開できます"))
		}
		line(sAlertBox.Width(inner - 2).Render(strings.Join(msg, "\n")))
	} else if m.aiIdle {
		msg := []string{sWarn.Render("💤 業務連絡: AIが暇しています")}
		for _, a := range m.agents {
			msg = append(msg, fmt.Sprintf("  %s %s（%s / %s）",
				statusIcon(a.Status, m.spin), a.Name, a.Status, a.Where))
		}
		if m.aiTyping {
			msg = append(msg, sGood.Render("⌨ 指示の入力を検知（メーター回復中）"))
		} else {
			msg = append(msg, sSub.Render("次のタスクを振るとゲームを再開できます（指示の入力中は評価も回復）"))
		}
		line(sBreakBox.Width(inner - 2).Render(strings.Join(msg, "\n")))
	} else {
		line(sPhraseBox.Width(inner - 2).Render(m.renderPhrase()))
	}

	// フラッシュメッセージ
	if m.flashText != "" && time.Since(m.flashAt) < 1500*time.Millisecond {
		line(" " + sWarn.Render(m.flashText))
	} else {
		line("")
	}
	line("")

	// AIステータスパネル
	line(sTitle.Render(" 🤖 監視対象AI"))
	if len(m.agents) == 0 {
		if m.cfg.Herdr {
			line(sSub.Render("  （このワークスペースに稼働中のエージェントはいません）"))
		} else {
			line(sSub.Render("  （監視対象なし: herdr ペイン内で起動するか --status-file 連携を設定）"))
		}
	} else {
		for _, a := range m.agents {
			note := ""
			if a.Note != "" {
				note = " 「" + a.Note + "」"
			}
			line(fmt.Sprintf("  %s %-8s %-8s %s%s",
				statusIcon(a.Status, m.spin), a.Name,
				statusStyle(a.Status).Render(a.Status),
				sSub.Render(a.Where+" ("+a.Source+")"), sSub.Render(note)))
		}
	}
	line("")
	line(sSub.Render(" tab: お題スキップ ・ esc: 休憩 ・ ctrl+c: 退勤"))

	return b.String()
}

// kpmView は直近15秒の正打から打/分を算出する（keyTimes は tick 側で刈り込み済み）。
func (m model) kpmView() int {
	cutoff := time.Now().Add(-kpmWindow)
	n := 0
	for _, t := range m.keyTimes {
		if t.After(cutoff) {
			n++
		}
	}
	return n * int(time.Minute/kpmWindow)
}

func (m model) renderPhrase() string {
	target := []rune(m.phrase)
	typed := sTyped.Render(string(target[:m.pos]))
	curStyle := sCursor
	if time.Since(m.wrongAt) < wrongFlash {
		curStyle = sWrong
	}
	cur := curStyle.Render(string(target[m.pos]))
	rest := ""
	if m.pos+1 < len(target) {
		rest = sRest.Render(string(target[m.pos+1:]))
	}
	return typed + cur + rest
}

func gauge(width int, ratio float64, color lipgloss.Color) string {
	ratio = max(0, min(1, ratio))
	filled := int(float64(width) * ratio)
	bar := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled)) +
		sSub.Render(strings.Repeat("░", width-filled))
	return "[" + bar + "]"
}

func statusIcon(status string, spin int) string {
	switch status {
	case "working":
		return sGood.Render(spinnerFrames[spin])
	case "blocked":
		return sBad.Render("✋")
	case "done":
		return sWarn.Render("✔")
	case "idle":
		return sSub.Render("●")
	default:
		return sSub.Render("?")
	}
}

func statusStyle(status string) lipgloss.Style {
	switch status {
	case "working":
		return sGood
	case "blocked":
		return sBad
	case "done":
		return sWarn
	default:
		return sSub
	}
}

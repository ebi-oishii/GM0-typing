package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	evalStart     = 60.0
	evalMax       = 100.0
	evalGainKey   = 0.4  // 正打1回の回復量
	evalLossMiss  = 1.5  // ミス1回の減点
	evalDecayIdle = 0.08 // 手が止まっている間の減衰 (per 100ms)
	evalDecayWait = 0.30 // AIを待たせている間の減衰 (per 100ms)
	evalGainAIKey = 0.20 // AIペインへの指示入力中の回復 (per 100ms) — 指示出しは立派な打鍵
	idleAfter     = 1500 * time.Millisecond
	kpmWindow     = 15 * time.Second
	wrongFlash    = 350 * time.Millisecond
	bonusPerClear = 15 // お題クリアボーナス（倍率が掛かる）
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type tickMsg time.Time
type pollMsg aiPoll

type model struct {
	cfg    Config
	width  int
	height int

	// お題
	order  []int
	orderI int
	phrase string
	pos    int

	// セッション成績
	taps     int // 全打鍵（ミス含む・実測）
	miss     int
	reported int // 申告用入力数（倍率込み）
	combo    int
	maxCombo int
	keyTimes []time.Time // KPM算出用（正打のみ）

	// 日次成績（前セッションからの持ち越し）
	baseTaps     int
	baseReported int
	baseMiss     int

	eval      float64
	lastKeyAt time.Time
	wrongAt   time.Time
	flashText string
	flashAt   time.Time

	// AI状態
	agents        []Agent
	multiplier    int
	alert         bool
	aiIdle        bool // AIはいるが誰も働いていない（仕事を振るまでロック）
	aiTyping      bool   // AIペインへの指示入力中と推定（評価上昇）
	lastFocusPane string // 前回ポーリングでフォーカスされていたエージェントペイン
	lastTailHash  uint64 // そのペインの前回の画面ハッシュ

	// 休憩（プロセスは生かしたまま時を止める）
	paused     bool
	pauseStart time.Time

	// 出社回帰（評価メーター0 = ゲームオーバー）
	gameOver    bool
	recalls     int // 今セッションの出社回帰回数
	baseRecalls int // 本日の持ち越し分

	spin      int
	saveEvery int
	startAt   time.Time
}

func newModel(cfg Config) model {
	m := model{
		cfg:        cfg,
		eval:       evalStart,
		multiplier: 1,
		startAt:    time.Now(),
		order:      rand.Perm(len(phrases)),
	}
	m.loadStats()
	m.phrase = phrases[m.order[0]]
	return m
}

func (m *model) nextPhrase() {
	m.orderI++
	if m.orderI >= len(m.order) {
		m.order = rand.Perm(len(phrases))
		m.orderI = 0
	}
	m.phrase = phrases[m.order[m.orderI]]
	m.pos = 0
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), pollCmd(m.cfg, 0))
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func pollCmd(cfg Config, delay time.Duration) tea.Cmd {
	if delay == 0 {
		return func() tea.Msg { return pollMsg(pollAgents(cfg)) }
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return pollMsg(pollAgents(cfg)) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.spin = (m.spin + 1) % len(spinnerFrames)
		cutoff := time.Now().Add(-kpmWindow)
		trim := 0
		for trim < len(m.keyTimes) && m.keyTimes[trim].Before(cutoff) {
			trim++
		}
		m.keyTimes = m.keyTimes[trim:]
		switch {
		case m.paused, m.gameOver:
			// 休憩中と出社中は全て凍結。減衰も回復もしない
		case m.aiTyping:
			// AIペインで指示を入力中 — 指示出しは立派な打鍵なのでバーが増える。
			// フォーカスしているだけ（タイピングなし）なら下の減衰に落ちる。
			// ここでの評価基準はあくまでタイピングである。
			m.eval = min(evalMax, m.eval+evalGainAIKey)
		case m.alert:
			m.eval -= evalDecayWait
		case time.Since(m.lastKeyAt) > idleAfter:
			m.eval -= evalDecayIdle
		}
		if m.eval < 0 {
			m.eval = 0
		}
		if m.eval == 0 && !m.gameOver && !m.paused {
			// タイピング量の低下が人事に検知された
			m.gameOver = true
			m.recalls++
		}
		return m, tickCmd()

	case pollMsg:
		m.agents = msg.Agents
		// 同じエージェントペインにフォーカスし続けていて、入力欄領域が
		// 変化した → ユーザーの指示入力と推定（生成中の出力と分離できるかは
		// 採取側 shouldSample が判断済み。採取できなかったポーリングは 0）
		m.aiTyping = msg.FocusPaneID != "" &&
			msg.FocusPaneID == m.lastFocusPane &&
			msg.FocusTailHash != 0 && m.lastTailHash != 0 &&
			msg.FocusTailHash != m.lastTailHash
		m.lastFocusPane = msg.FocusPaneID
		m.lastTailHash = msg.FocusTailHash
		working := 0
		attention := 0
		for _, a := range m.agents {
			if a.Status == "working" {
				working++
			}
			if a.NeedsAttention() {
				attention++
			}
		}
		m.alert = attention > 0
		// AIのジョブが終わった後に遊び続けるのは許されない。
		// 監視対象がいるのに誰も働いていなければ、次の仕事を振るまでロック。
		m.aiIdle = len(m.agents) > 0 && working == 0 && attention == 0
		if m.alert || m.aiIdle {
			m.multiplier = 0
		} else {
			m.multiplier = 1 + working
		}
		m.saveEvery++
		if m.saveEvery%5 == 0 { // 約10秒ごとに日次成績を保存
			m.saveStats()
		}
		return m, pollCmd(m.cfg, m.cfg.PollInterval)

	case tea.KeyMsg:
		if m.gameOver {
			// 出社中はオフィスからの操作のみ受け付ける
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyEnter:
				m.gameOver = false
				m.eval = evalStart
				m.combo = 0
				m.nextPhrase()
				m.flash("🏢 再出社しました。挽回しましょう")
			}
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC: // 退勤（終了してサマリを表示）
			return m, tea.Quit
		case tea.KeyEsc: // 休憩トグル（プロセスは殺さず、時を止めるだけ）
			m.paused = !m.paused
			if m.paused {
				m.pauseStart = time.Now()
			}
			return m, nil
		case tea.KeyTab:
			if m.paused {
				return m, nil
			}
			m.combo = 0
			m.nextPhrase()
			return m, nil
		case tea.KeyRunes, tea.KeySpace:
			if m.paused {
				m.flash("☕ 休憩中です（esc で業務再開）")
				return m, nil
			}
			if m.alert {
				// AIを待たせている間の打鍵は「サボり」なのでカウントしない
				m.flash("⚠ AI対応が先です（この打鍵はカウントされません）")
				return m, nil
			}
			if m.aiIdle {
				m.flash("💤 AIが暇しています（次のタスクを振ってください）")
				return m, nil
			}
			s := msg.String()
			if msg.Type == tea.KeySpace {
				s = " "
			}
			// ペーストによる打鍵数水増しを防ぐため、1イベント1打鍵のみ受理
			r := []rune(s)[0]
			m.handleKey(r)
			return m, nil
		}
	}
	return m, nil
}

func (m *model) handleKey(r rune) {
	m.taps++
	m.lastKeyAt = time.Now()
	target := []rune(m.phrase)
	if r == target[m.pos] {
		m.pos++
		m.reported += m.multiplier
		m.combo++
		if m.combo > m.maxCombo {
			m.maxCombo = m.combo
		}
		m.eval = min(evalMax, m.eval+evalGainKey)
		m.keyTimes = append(m.keyTimes, time.Now())
		if m.pos >= len(target) {
			bonus := bonusPerClear * m.multiplier
			m.reported += bonus
			m.flash(fmt.Sprintf("✓ お題クリア ボーナス +%d", bonus))
			m.nextPhrase()
		}
	} else {
		m.miss++
		m.combo = 0
		m.eval = max(0, m.eval-evalLossMiss)
		m.wrongAt = time.Now()
	}
}

func (m *model) flash(s string) {
	m.flashText = s
	m.flashAt = time.Now()
}

// accuracy は日次累計の正確率（表示している実測・ミスと単位を揃える）。
func (m model) accuracy() float64 {
	if m.dayTaps() == 0 {
		return 100
	}
	return float64(m.dayTaps()-m.dayMiss()) / float64(m.dayTaps()) * 100
}

func (m model) dayReported() int { return m.baseReported + m.reported }
func (m model) dayTaps() int     { return m.baseTaps + m.taps }
func (m model) dayMiss() int     { return m.baseMiss + m.miss }

func rankOf(reported int) (string, string) {
	switch {
	case reported >= 25000:
		return "SS", "人事も感動"
	case reported >= 10000:
		return "S", "昇進圏内"
	case reported >= 5000:
		return "A", "評価良好"
	case reported >= 2000:
		return "B", "現状維持"
	default:
		return "C", "査定危険"
	}
}

// nextRank は次のランクまでの残り打数と次ランク名を返す。SS 到達済みなら ("", 0)。
func nextRank(reported int) (int, string) {
	thresholds := []struct {
		at   int
		name string
	}{{2000, "B"}, {5000, "A"}, {10000, "S"}, {25000, "SS"}}
	for _, t := range thresholds {
		if reported < t.at {
			return t.at - reported, t.name
		}
	}
	return 0, ""
}

func evalLabel(v float64) (string, lipgloss.Color) {
	switch {
	case v >= 80:
		return "順風満帆", cGreen
	case v >= 50:
		return "平常運転", cCyan
	case v >= 25:
		return "雲行き怪しい", cYellow
	default:
		return "PIP候補", cRed
	}
}

// --- 日次成績の永続化 ---

type dayStats struct {
	Date     string `json:"date"`
	Taps     int    `json:"taps"`
	Reported int    `json:"reported"`
	Miss     int    `json:"miss"`
	Recalls  int    `json:"recalls"` // 出社回帰回数
}

func today() string { return time.Now().Format("2006-01-02") }

func (m *model) loadStats() {
	data, err := os.ReadFile(m.cfg.StatsFile)
	if err != nil {
		return
	}
	var s dayStats
	if json.Unmarshal(data, &s) != nil || s.Date != today() {
		return
	}
	m.baseTaps, m.baseReported, m.baseMiss = s.Taps, s.Reported, s.Miss
	m.baseRecalls = s.Recalls
}

func (m model) saveStats() {
	s := dayStats{
		Date:     today(),
		Taps:     m.dayTaps(),
		Reported: m.dayReported(),
		Miss:     m.dayMiss(),
		Recalls:  m.baseRecalls + m.recalls,
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(m.cfg.StatsFile, data, 0o644)
}

func (m model) summary() string {
	rank, label := rankOf(m.dayReported())
	acc := fmt.Sprintf("%.1f%%", m.accuracy())
	closing := "今日も評価を守り切った。おつかれさまでした。"
	recallLine := ""
	if recalls := m.baseRecalls + m.recalls; recalls > 0 {
		recallLine = fmt.Sprintf(" 出社回帰 %d回\n", recalls)
		closing = "明日はリモートを守り抜きましょう。"
	}
	if m.gameOver {
		// 出社回帰のまま退勤 = 問答無用で最低評価。打鍵数がいくらあっても関係ない
		rank, label = "F", "出社回帰"
		closing = "タイピング量の低下により最低評価となりました。明日はオフィスでお会いしましょう。"
	}
	return fmt.Sprintf(`
──────────── 退勤サマリ ────────────
 本日の申告用入力数   %s 打
 実測 %s 打（うち今セッション %s 打）
 正確率 %s ・ 最大コンボ %d
 査定ランク %s「%s」
%s
 %s
────────────────────────────────────
`, humanInt(m.dayReported()), humanInt(m.dayTaps()), humanInt(m.taps), acc, m.maxCombo, rank, label, recallLine, closing)
}

func humanInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}

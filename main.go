// GM0-typing — 評価防衛型タイピングゲーム
//
// AI時代、エンジニアの生産性がタイピング量で測られてしまったあなたへ。
// 右のペインでAIを働かせ、左のペインで打鍵数を稼ぎ、評価を守り抜け。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Config はゲームの起動設定。
type Config struct {
	Herdr        bool          // herdr CLI でペインのエージェント状態をポーリングする
	OwnPaneID    string        // 自分が動いているペイン（監視対象から除外する）
	WorkspaceID  string        // 監視対象のワークスペース
	StatusFile   string        // herdr 外連携用のステータスファイル
	PollInterval time.Duration // AI状態のポーリング間隔
	StatsFile    string        // 日次成績の保存先
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "share", "gm0-typing")
}

func main() {
	dataDir := defaultDataDir()
	statusFile := flag.String("status-file", filepath.Join(dataDir, "status.json"),
		"AIステータス連携用JSONファイル（Claude Code hooks など herdr 外での利用向け）")
	noHerdr := flag.Bool("no-herdr", false, "herdr 連携を無効化する")
	poll := flag.Duration("poll", 2*time.Second, "AIステータスのポーリング間隔")
	flag.Parse()

	cfg := Config{
		Herdr:        !*noHerdr && os.Getenv("HERDR_ENV") == "1",
		OwnPaneID:    os.Getenv("HERDR_PANE_ID"),
		WorkspaceID:  os.Getenv("HERDR_WORKSPACE_ID"),
		StatusFile:   *statusFile,
		PollInterval: *poll,
		StatsFile:    filepath.Join(dataDir, "stats.json"),
	}
	_ = os.MkdirAll(dataDir, 0o755)

	p := tea.NewProgram(newModel(cfg), tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gm0-typing:", err)
		os.Exit(1)
	}
	if m, ok := finalModel.(model); ok {
		m.saveStats()
		fmt.Print(m.summary())
	}
}

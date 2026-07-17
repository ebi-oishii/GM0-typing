package main

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Agent は監視対象のAI 1体の状態。
type Agent struct {
	Name   string // claude, codex, ...
	Status string // working / blocked / done / idle / unknown
	Where  string // ペインID やファイル名など、どこにいるか
	Note   string // カスタムステータス等の補足
	Source string // herdr / file
}

// NeedsAttention は「人間の対応待ち」かどうか。
// blocked は入力待ち、done は完了未確認。どちらも放置してゲームで遊ぶのは言い訳できない。
func (a Agent) NeedsAttention() bool {
	return a.Status == "blocked" || a.Status == "done"
}

// aiPoll は1回のポーリング結果。
type aiPoll struct {
	Agents []Agent
	// FocusPaneID はユーザーがフォーカス中のエージェントペイン（なければ空）。
	FocusPaneID string
	// FocusTailHash はフォーカス中ペインの入力欄領域のハッシュ。herdr にキー入力
	// APIはないため、前回ポーリングとの変化で「AIへの指示入力中」を推定する。
	// 0 は「採取できなかった」を意味し、判定に使わない。
	FocusTailHash uint64
}

// shouldSample は画面ハッシュを判定に使ってよいかを返す。
// 入力欄（composer）を特定できていれば、エージェントの出力と分離できるので
// working 中（回答の生成中）でも採取できる。特定できない場合は画面全体の
// ハッシュしかなく、生成中は出力で常に変化してしまうため非稼働時のみ採取する。
func shouldSample(composer bool, status string) bool {
	return composer || status != "working"
}

func pollAgents(cfg Config) aiPoll {
	var poll aiPoll
	if cfg.Herdr {
		agents, focusPane, focusStatus := herdrAgents(cfg)
		poll.Agents = append(poll.Agents, agents...)
		poll.FocusPaneID = focusPane
		if focusPane != "" {
			hash, composer := herdrTailHash(focusPane)
			if shouldSample(composer, focusStatus) {
				poll.FocusTailHash = hash
			}
		}
	}
	poll.Agents = append(poll.Agents, fileAgents(cfg.StatusFile)...)
	sort.SliceStable(poll.Agents, func(i, j int) bool { return poll.Agents[i].Where < poll.Agents[j].Where })
	return poll
}

// --- herdr 連携 ---

type herdrPaneList struct {
	Result struct {
		Panes []struct {
			Agent        string `json:"agent"`
			AgentStatus  string `json:"agent_status"`
			PaneID       string `json:"pane_id"`
			Focused      bool   `json:"focused"`
			CustomStatus string `json:"custom_status"`
			Title        string `json:"title"`
		} `json:"panes"`
	} `json:"result"`
}

func herdrAgents(cfg Config) (agents []Agent, focusPane, focusStatus string) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	args := []string{"pane", "list"}
	if cfg.WorkspaceID != "" {
		args = append(args, "--workspace", cfg.WorkspaceID)
	}
	out, err := exec.CommandContext(ctx, "herdr", args...).Output()
	if err != nil {
		return nil, "", ""
	}
	return parseHerdrPanes(out, cfg.OwnPaneID)
}

func parseHerdrPanes(data []byte, ownPane string) (agents []Agent, focusPane, focusStatus string) {
	var list herdrPaneList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, "", ""
	}
	for _, p := range list.Result.Panes {
		if p.Agent == "" || p.PaneID == ownPane {
			continue
		}
		if p.Focused {
			focusPane = p.PaneID
			focusStatus = p.AgentStatus
		}
		note := p.CustomStatus
		if note == "" {
			note = p.Title
		}
		agents = append(agents, Agent{
			Name:   p.Agent,
			Status: p.AgentStatus,
			Where:  p.PaneID,
			Note:   note,
			Source: "herdr",
		})
	}
	return agents, focusPane, focusStatus
}

// herdrTailHash はペイン表示内容のハッシュと、入力欄を特定できたかを返す
// （hash 0 は取得失敗）。
func herdrTailHash(paneID string) (uint64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "herdr",
		"pane", "read", paneID, "--source", "visible", "--lines", "30").Output()
	if err != nil {
		return 0, false
	}
	return tailHash(out)
}

// composerWindow は画面末尾から入力欄プロンプトを探す行数。
// 複数行プロンプト（改行・折り返し）では ❯ は入力欄の先頭行にしか付かないため、
// 長文入力中でも見失わないよう広めに取る。上方の会話ログに含まれる ❯ を
// 誤認しないよう、無制限にはしない。
const composerWindow = 25

// tailHash は画面テキストをハッシュ化し、入力欄を特定できたかを返す。
//
//   - 最下行は Claude Code などのステータスバーで、ヒント表示がタイピングと
//     無関係に回転するため常に除外する。
//   - 末尾付近に入力欄プロンプト（❯）があれば、その行から下だけを対象にする。
//     エージェントの出力・スピナー・トークンカウンタは入力欄より上に描画される
//     ため、生成中でもユーザーのタイピングだけを分離して検知できる。
//   - 見つからなければ画面全体（ステータスバー除く）を対象にする。
func tailHash(screen []byte) (uint64, bool) {
	lines := strings.Split(strings.TrimRight(string(screen), "\n"), "\n")
	if len(lines) > 1 {
		lines = lines[:len(lines)-1] // ステータスバーを除外
	}
	composer := false
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-composerWindow; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "❯") {
			lines = lines[i:]
			composer = true
			break
		}
	}
	h := fnv.New64a()
	h.Write([]byte(strings.Join(lines, "\n")))
	return h.Sum64(), composer
}

// --- ステータスファイル連携（herdr 外・Claude Code hooks 向け）---

type fileAgent struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Task   string `json:"task"`
}

type fileStatus struct {
	Agents []fileAgent `json:"agents"`
}

// fileAgents はステータスファイルを読む。10分以上更新がないファイルは
// 死んだ hook の残骸とみなして無視する（永遠の blocked を防ぐ）。
func fileAgents(path string) []Agent {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > 10*time.Minute {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var multi fileStatus
	if err := json.Unmarshal(data, &multi); err == nil && len(multi.Agents) > 0 {
		return toAgents(multi.Agents)
	}
	var single fileAgent
	if err := json.Unmarshal(data, &single); err == nil && single.Status != "" {
		return toAgents([]fileAgent{single})
	}
	return nil
}

func toAgents(fas []fileAgent) []Agent {
	var agents []Agent
	for _, fa := range fas {
		name := fa.Name
		if name == "" {
			name = "agent"
		}
		agents = append(agents, Agent{
			Name:   name,
			Status: fa.Status,
			Where:  "file",
			Note:   fa.Task,
			Source: "file",
		})
	}
	return agents
}

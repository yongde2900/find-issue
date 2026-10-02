// 每隔一段時間檢查 GitHub repo (預設 ray-project/kuberay) 是否有新的 issue。
//
// 使用前先複製設定檔: cp config.example.json config.json
//
// 用法:
//
//	go run .                        # 讀取 config.json，依設定的間隔持續檢查
//	go run . -once                  # 只檢查一次就結束
//	go run . -config other.json     # 指定其他設定檔
//
// 設定檔格式:
//
//	{
//	  "interval": "5m",
//	  "repos": ["ray-project/kuberay", "ray-project/ray"],
//	  "telegram": {"bot_token": "", "chat_id": "@my_channel"}
//	}
//
// telegram 為選填；bot_token 也可改用環境變數 TELEGRAM_BOT_TOKEN 設定 (優先於設定檔)。
//
// 可設定環境變數 GITHUB_TOKEN 以提高 API 速率限制；若未設定，會嘗試使用 `gh auth token`。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const stateFile = ".last_seen.json"

type telegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

type config struct {
	Interval time.Duration
	Repos    []string
	Telegram *telegramConfig
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Interval string          `json:"interval"`
		Repos    []string        `json:"repos"`
		Telegram *telegramConfig `json:"telegram"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析 %s 失敗: %w", path, err)
	}
	cfg := &config{Interval: 5 * time.Minute, Repos: raw.Repos, Telegram: raw.Telegram}
	if raw.Interval != "" {
		if cfg.Interval, err = time.ParseDuration(raw.Interval); err != nil {
			return nil, fmt.Errorf("interval 格式錯誤 (例如 \"5m\"): %w", err)
		}
	}
	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("%s 沒有設定任何 repos", path)
	}
	if tg := cfg.Telegram; tg != nil {
		if t := os.Getenv("TELEGRAM_BOT_TOKEN"); t != "" {
			tg.BotToken = t
		}
		if tg.BotToken == "" || tg.ChatID == "" {
			return nil, fmt.Errorf("telegram 需要 bot_token (或環境變數 TELEGRAM_BOT_TOKEN) 與 chat_id")
		}
	}
	return cfg, nil
}

type issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	HTMLURL     string          `json:"html_url"`
	PullRequest json.RawMessage `json:"pull_request"`
}

func getToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func fetchRecentIssues(client *http.Client, repo, token string) ([]issue, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=all&sort=created&direction=desc&per_page=50", repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 回傳 %s", resp.Status)
	}
	var items []issue
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	// /issues 也會回傳 PR，需要排除
	issues := items[:0]
	for _, it := range items {
		if it.PullRequest == nil {
			issues = append(issues, it)
		}
	}
	return issues, nil
}

func loadState() (map[string]int, error) {
	state := map[string]int{}
	data, err := os.ReadFile(stateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	return state, json.Unmarshal(data, &state)
}

func saveState(state map[string]int) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0o644)
}

func notify(title, message string) {
	if runtime.GOOS != "darwin" {
		return
	}
	// strconv.Quote 會把非 ASCII 轉成 \u 跳脫，AppleScript 看不懂，所以手動跳脫
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc.Replace(message), esc.Replace(title))
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Printf("通知失敗: %v", err)
	}
}

func check(client *http.Client, repo, token string, tg *telegram) error {
	issues, err := fetchRecentIssues(client, repo, token)
	if err != nil || len(issues) == 0 {
		return err
	}
	state, err := loadState()
	if err != nil {
		return err
	}

	newest := 0
	for _, it := range issues {
		newest = max(newest, it.Number)
	}

	lastSeen, ok := state[repo]
	if !ok {
		log.Printf("[%s] 首次執行，記錄目前最新 issue #%d，之後只通知新的 issue", repo, newest)
		state[repo] = newest
		return saveState(state)
	}

	var newIssues []issue
	for _, it := range issues {
		if it.Number > lastSeen {
			newIssues = append(newIssues, it)
		}
	}
	if len(newIssues) == 0 {
		log.Printf("[%s] 沒有新的 issue", repo)
		return nil
	}

	sort.Slice(newIssues, func(i, j int) bool { return newIssues[i].Number < newIssues[j].Number })
	for _, it := range newIssues {
		log.Printf("[%s] 新 issue #%d: %s\n    %s", repo, it.Number, it.Title, it.HTMLURL)
		notify(repo+" 新 issue #"+strconv.Itoa(it.Number), it.Title)
		if tg != nil {
			if err := tg.send(repo, it); err != nil {
				log.Printf("[%s] Telegram 通知 #%d 失敗: %v", repo, it.Number, err)
			}
		}
	}
	state[repo] = newest
	return saveState(state)
}

func main() {
	configPath := flag.String("config", "config.json", "設定檔路徑")
	once := flag.Bool("once", false, "只檢查一次")
	flag.Parse()

	log.SetFlags(log.LstdFlags)
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("讀取設定檔失敗: %v", err)
	}
	token := getToken()
	client := &http.Client{Timeout: 30 * time.Second}

	var tg *telegram
	if cfg.Telegram != nil {
		tg = &telegram{client: client, apiBase: telegramAPIBase, botToken: cfg.Telegram.BotToken, chatID: cfg.Telegram.ChatID}
	}

	log.Printf("監看 %s，間隔 %s", strings.Join(cfg.Repos, ", "), cfg.Interval)
	if tg != nil {
		log.Printf("新 issue 會通知到 Telegram %s", tg.chatID)
	}
	if token == "" {
		// 未認證每小時只能呼叫 60 次 API，每個 repo 每次檢查會用掉 1 次
		if perHour := float64(len(cfg.Repos)) * float64(time.Hour) / float64(cfg.Interval); perHour > 60 {
			log.Printf("警告: 未使用 token，速率限制 60 次/小時，目前設定每小時需 %.0f 次，請設定 GITHUB_TOKEN 或執行 gh auth login", perHour)
		} else {
			log.Print("未使用 token，速率限制 60 次/小時")
		}
	}

	for {
		for _, repo := range cfg.Repos {
			if err := check(client, repo, token, tg); err != nil {
				log.Printf("[%s] 檢查失敗: %v", repo, err)
			}
		}
		if *once {
			return
		}
		time.Sleep(cfg.Interval)
	}
}

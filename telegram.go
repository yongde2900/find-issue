package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
)

const telegramAPIBase = "https://api.telegram.org"

type telegram struct {
	client   *http.Client
	apiBase  string
	botToken string
	chatID   string // 頻道用 "@channel_name" 或 "-100..." 數字 ID
}

func (t *telegram) send(repo string, it issue) error {
	text := fmt.Sprintf("<b>%s</b> 新 issue #%d\n%s\n%s",
		html.EscapeString(repo), it.Number, html.EscapeString(it.Title), html.EscapeString(it.HTMLURL))
	resp, err := t.client.PostForm(t.apiBase+"/bot"+t.botToken+"/sendMessage", url.Values{
		"chat_id":    {t.chatID},
		"text":       {text},
		"parse_mode": {"HTML"},
	})
	if err != nil {
		// 錯誤訊息內含 URL，避免把 bot token 印到 log
		if uerr, ok := err.(*url.Error); ok {
			err = uerr.Err
		}
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("Telegram 回應無法解析 (%s): %w", resp.Status, err)
	}
	if !result.OK {
		return fmt.Errorf("Telegram API 錯誤: %s", result.Description)
	}
	return nil
}

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSend(t *testing.T) {
	var gotPath, gotChat, gotText, gotMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotChat, gotText, gotMode = r.FormValue("chat_id"), r.FormValue("text"), r.FormValue("parse_mode")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), apiBase: srv.URL, botToken: "123:abc", chatID: "@my_channel"}
	err := tg.send("ray-project/kuberay", issue{Number: 42, Title: "a < b & c", HTMLURL: "https://github.com/ray-project/kuberay/issues/42"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot123:abc/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if gotChat != "@my_channel" || gotMode != "HTML" {
		t.Errorf("chat_id = %q, parse_mode = %q", gotChat, gotMode)
	}
	if !strings.Contains(gotText, "#42") || !strings.Contains(gotText, "a &lt; b &amp; c") {
		t.Errorf("text = %q", gotText)
	}
}

func TestTelegramSendAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), apiBase: srv.URL, botToken: "123:abc", chatID: "@nope"}
	err := tg.send("a/b", issue{Number: 1, Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("err = %v", err)
	}
}

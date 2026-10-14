package schedule

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoadAndSaveAI(t *testing.T) {
	t.Setenv("WEIXIN_AI_BASE", "")
	t.Setenv("WEIXIN_AI_KEY", "")
	t.Setenv("WEIXIN_AI_MODEL", "")
	dir := t.TempDir()
	if err := SaveAI(dir, AIConfig{Base: "https://example.com/v1/", Key: "secret", Model: "demo"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAI(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Base != "https://example.com/v1" || cfg.Key != "secret" || cfg.Model != "demo" {
		t.Fatalf("保存后读回来不对: %+v", cfg)
	}

	t.Setenv("WEIXIN_AI_MODEL", "from-env")
	cfg, err = LoadAI(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "from-env" || cfg.Key != "secret" {
		t.Fatalf("环境变量应盖过文件里的模型名: %+v", cfg)
	}

	if err := os.WriteFile(dir+"/ai.json", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEIXIN_AI_BASE", "")
	t.Setenv("WEIXIN_AI_KEY", "")
	t.Setenv("WEIXIN_AI_MODEL", "")
	if _, err := LoadAI(dir); err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("损坏的配置应报错: %v", err)
	}
	if err := SaveAI(dir, AIConfig{Base: "http://user:secret@example.com/v1", Key: "secret", Model: "demo"}); err == nil {
		t.Fatal("地址里不该带密钥")
	}
}

func TestCompleteUsesChatAPI(t *testing.T) {
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("请求不是 JSON: %v", err)
		}
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"今天晴"}}]}`))
	}))
	defer srv.Close()

	cfg := AIConfig{Base: srv.URL + "/v1/chat/completions", Key: "test-key", Model: "demo"}
	text, err := cfg.Complete(context.Background(), "系统", "问题")
	if err != nil {
		t.Fatal(err)
	}
	if text != "今天晴" || gotAuth != "Bearer test-key" || gotModel != "demo" {
		t.Fatalf("返回不对: %s %s %s", text, gotAuth, gotModel)
	}
}

func TestModelErrorHidesKey(t *testing.T) {
	err := modelError(401, []byte(`{"error":{"message":"bad key sk-test"}}`), "sk-test")
	if err == nil || strings.Contains(err.Error(), "sk-test") {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
	err = modelError(400, []byte(`{"error":{"message":"model not found"}}`), "sk-test")
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatal(err)
	}
}

package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/tidwall/sjson"
)

//go:embed web/*
var webFS embed.FS

const (
	defaultPort = "1450"
	defaultUser = "admin"
	defaultPass = "claw520"
	cookieName  = "clawpanel_session"
)

var (
	gOpenclawBin string
	gProfile     string
)

type configStatus struct {
	ConfigPath string `json:"configPath"`
	Found      bool   `json:"found"`
}

type configPayload struct {
	Model   string `json:"model"`
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseUrl"`
}

type chatPayload struct {
	Message string `json:"message"`
	Model   string `json:"model"`
}

type statusResp struct {
	OpenClaw string `json:"openclaw"`
	Gateway  string `json:"gateway"`
}

type logsResp struct {
	Output string `json:"output"`
}

type systemResp struct {
	Uname  string `json:"uname"`
	Uptime string `json:"uptime"`
	Disk   string `json:"disk"`
	Mem    string `json:"mem"`
}

type cronPayload struct {
	Text     string `json:"text"`
	Minutes  int    `json:"minutes"`
	JobName  string `json:"jobName"`
	Mode     string `json:"mode"` // once|every
}

type apiTemplate struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
}

type channelPayload struct {
	TelegramToken string `json:"telegramToken"`
	TelegramAllow string `json:"telegramAllow"`
	QQAppID       string `json:"qqAppId"`
	QQAppKey      string `json:"qqAppKey"`
	QQBotToken    string `json:"qqBotToken"`
	QQAllow       string `json:"qqAllow"`
	DiscordToken  string `json:"discordToken"`
	DiscordGuild  string `json:"discordGuild"`
	DiscordUser   string `json:"discordUser"`
}

type skillsPayload struct {
	Action string `json:"action"` // list|check|info
	Skill  string `json:"skill"`
}

type pairingPayload struct {
	Channel string `json:"channel"`
	Code    string `json:"code"`
}

type browserPayload struct {
	URL  string `json:"url"`
	Path string `json:"path"`
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	port := envOr("CLAWPANEL_PORT", defaultPort)
	user := envOr("CLAWPANEL_USER", defaultUser)
	pass := envOr("CLAWPANEL_PASS", defaultPass)
	bin := envOr("CLAWPANEL_OPENCLAW_BIN", "openclaw")
	installScript := envOr("CLAWPANEL_INSTALL_SCRIPT", "https://openclaw.ai/install.sh")
	profile := envOr("CLAWPANEL_PROFILE", "")

	gOpenclawBin = bin
	gProfile = profile

	key1 := securecookie.GenerateRandomKey(32)
	key2 := securecookie.GenerateRandomKey(32)
	sc := securecookie.New(key1, key2)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !isAuthed(r, sc) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		serveFile(w, r, "index.html")
	})

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			serveFile(w, r, "login.html")
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			u := r.Form.Get("username")
			p := r.Form.Get("password")
			if subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1 && subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1 {
				value := map[string]string{"u": u}
				if encoded, err := sc.Encode(cookieName, value); err == nil {
					http.SetCookie(w, &http.Cookie{Name: cookieName, Value: encoded, Path: "/", HttpOnly: true})
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
			serveFile(w, r, "login.html", "?error=1")
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1})
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	mux.HandleFunc("/api/config/status", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		path, found := detectConfigPath()
		writeJSON(w, configStatus{ConfigPath: path, Found: found})
	}))

	mux.HandleFunc("/api/config/get", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		path, found := detectConfigPath()
		if !found {
			http.Error(w, "config not found", http.StatusNotFound)
			return
		}
		cfg, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "failed to read config", http.StatusInternalServerError)
			return
		}
		payload := extractConfig(cfg)
		writeJSON(w, payload)
	}))

	mux.HandleFunc("/api/config/set", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path, found := detectConfigPath()
		if !found {
			http.Error(w, "config not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload configPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		if err := updateConfigFile(path, payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/channels/get", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		path, found := detectConfigPath()
		if !found {
			http.Error(w, "config not found", http.StatusNotFound)
			return
		}
		cfg, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "failed to read config", http.StatusInternalServerError)
			return
		}
		clean := jsonc.ToJSON(cfg)
		discordGuild, discordUsers := firstGuild(clean)
		payload := channelPayload{
			TelegramToken: gjson.GetBytes(clean, "channels.telegram.botToken").String(),
			TelegramAllow: joinArray(gjson.GetBytes(clean, "channels.telegram.allowFrom")),
			QQAppID:       gjson.GetBytes(clean, "channels.qq.appId").String(),
			QQAppKey:      gjson.GetBytes(clean, "channels.qq.appKey").String(),
			QQBotToken:    gjson.GetBytes(clean, "channels.qq.botToken").String(),
			QQAllow:       joinArray(gjson.GetBytes(clean, "channels.qq.allowFrom")),
			DiscordToken:  gjson.GetBytes(clean, "channels.discord.token").String(),
			DiscordGuild:  discordGuild,
			DiscordUser:   discordUsers,
		}
		writeJSON(w, payload)
	}))

	mux.HandleFunc("/api/channels/set", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path, found := detectConfigPath()
		if !found {
			http.Error(w, "config not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload channelPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := updateChannels(path, payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/templates", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		templates := []apiTemplate{
			{Name: "通义千问", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-turbo"},
			{Name: "智谱 GLM", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4"},
			{Name: "百川", BaseURL: "https://api.baichuan-ai.com/v1", Model: "baichuan2-53b"},
			{Name: "讯飞星火", BaseURL: "https://spark-api-open.xf-yun.com/v1", Model: "spark-lite"},
			{Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
		}
		writeJSON(w, templates)
	}))

	mux.HandleFunc("/api/status", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		openclaw := runCmd(bin, withProfile(profile, "status")...)
		gateway := runCmd(bin, withProfile(profile, "gateway", "status")...)
		writeJSON(w, statusResp{OpenClaw: openclaw, Gateway: gateway})
	}))

	mux.HandleFunc("/api/system", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		sys := systemResp{
			Uname:  runCmd("bash", "-lc", "uname -a"),
			Uptime: runCmd("bash", "-lc", "uptime"),
			Disk:   runCmd("bash", "-lc", "df -h /"),
			Mem:    runCmd("bash", "-lc", "free -h"),
		}
		writeJSON(w, sys)
	}))

	mux.HandleFunc("/api/logs", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "logs", "--limit", "200", "--plain")...)
		writeJSON(w, logsResp{Output: out})
	}))

	mux.HandleFunc("/api/gateway/restart", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out := runCmd(bin, withProfile(profile, "gateway", "restart")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/install", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out := runCmd("bash", "-lc", "curl -fsSL "+installScript+" | bash")
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/chat", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload chatPayload
		if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.Message) == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}

		args := withProfile(profile, "agent", "--message", payload.Message, "--json")
		if strings.TrimSpace(payload.Model) != "" {
			args = append(args, "--model", payload.Model)
		}
		out := runCmd(bin, args...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/cron/add", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload cronPayload
		if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.Text) == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if payload.Minutes <= 0 {
			payload.Minutes = 30
		}
		if payload.JobName == "" {
			payload.JobName = "panel-job"
		}
		mode := strings.ToLower(strings.TrimSpace(payload.Mode))
		if mode == "" {
			mode = "every"
		}

		sched := "{\"kind\":\"every\",\"everyMs\":" + fmt.Sprintf("%d", payload.Minutes*60*1000) + "}"
		if mode == "once" {
			// one-shot in minutes
			at := time.Now().Add(time.Duration(payload.Minutes) * time.Minute).Format(time.RFC3339)
			sched = "{\"kind\":\"at\",\"at\":\"" + at + "\"}"
		}
		job := "{\"name\":\"" + payload.JobName + "\",\"schedule\":" + sched + ",\"payload\":{\"kind\":\"systemEvent\",\"text\":\"" + escapeJSON(payload.Text) + "\"},\"sessionTarget\":\"main\"}"

		out := runCmd(bin, withProfile(profile, "cron", "add", "--job", job)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/skills", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload skillsPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		action := strings.ToLower(strings.TrimSpace(payload.Action))
		if action == "" {
			action = "list"
		}
		args := withProfile(profile, "skills", action)
		if action == "info" && strings.TrimSpace(payload.Skill) != "" {
			args = append(args, payload.Skill)
		}
		out := runCmd(bin, args...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/pairing/list", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		ch := r.URL.Query().Get("channel")
		if ch == "" {
			ch = "telegram"
		}
		out := runCmd(bin, withProfile(profile, "pairing", "list", ch)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/pairing/approve", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload pairingPayload
		if err := json.Unmarshal(body, &payload); err != nil || payload.Channel == "" || payload.Code == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		out := runCmd(bin, withProfile(profile, "pairing", "approve", payload.Channel, payload.Code)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/extension", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out := runCmd(bin, withProfile(profile, "browser", "extension", "install")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/extension/path", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "browser", "extension", "path")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/status", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "browser", "status")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/start", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "browser", "start")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/stop", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "browser", "stop")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/open", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload browserPayload
		if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.URL) == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		out := runCmd(bin, withProfile(profile, "browser", "open", payload.URL)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/browser/screenshot", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload browserPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		path := payload.Path
		if strings.TrimSpace(path) == "" {
			path = "/tmp/openclaw_screenshot.png"
		}
		out := runCmd(bin, withProfile(profile, "browser", "screenshot", "--path", path)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	fs := http.FileServer(http.FS(webFS))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	addr := ":" + port
	log.Info().Msgf("ClawPanel Lite listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal().Err(err).Msg("server error")
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, name string, extra ...string) {
	path := "web/" + name
	b, err := webFS.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(extra) > 0 {
		w.Write(b)
		return
	}
	w.Write(b)
}

func isAuthed(r *http.Request, sc *securecookie.SecureCookie) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	val := map[string]string{}
	if err := sc.Decode(cookieName, c.Value, &val); err != nil {
		return false
	}
	_, ok := val["u"]
	return ok
}

func withAuth(sc *securecookie.SecureCookie, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAuthed(r, sc) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func detectConfigPath() (string, bool) {
	if v := os.Getenv("CLAWPANEL_CONFIG_PATH"); v != "" {
		if fileExists(v) {
			return v, true
		}
		return v, false
	}
	cand := []string{
		expand("~/.openclaw/openclaw.json"),
		"/etc/openclaw/openclaw.json",
	}
	for _, p := range cand {
		if fileExists(p) {
			return p, true
		}
	}
	return cand[0], false
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func extractConfig(raw []byte) configPayload {
	clean := jsonc.ToJSON(raw)
	model := gjson.GetBytes(clean, "agents.defaults.model.primary").String()
	apiKey := gjson.GetBytes(clean, "models.providers.custom.apiKey").String()
	baseURL := gjson.GetBytes(clean, "models.providers.custom.baseUrl").String()
	return configPayload{Model: model, APIKey: apiKey, BaseURL: baseURL}
}

func updateConfigFile(path string, payload configPayload) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config failed: %w", err)
	}

	backup := path + ".bak"
	_ = os.WriteFile(backup, raw, 0644)

	clean := jsonc.ToJSON(raw)
	if !gjson.ValidBytes(clean) {
		return errors.New("config json invalid")
	}

	updated, err := sjson.SetBytes(clean, "agents.defaults.model.primary", payload.Model)
	if err != nil {
		return err
	}
	updated, err = sjson.SetBytes(updated, "models.providers.custom.baseUrl", payload.BaseURL)
	if err != nil {
		return err
	}
	updated, err = sjson.SetBytes(updated, "models.providers.custom.apiKey", payload.APIKey)
	if err != nil {
		return err
	}
	updated, err = sjson.SetBytes(updated, "models.providers.custom.api", "openai-completions")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, updated, 0644); err != nil {
		return fmt.Errorf("write config failed: %w", err)
	}

	if out := runCmd(gOpenclawBin, withProfile(gProfile, "config", "validate")...); strings.Contains(strings.ToLower(out), "error") {
		return fmt.Errorf("config validate failed: %s", out)
	}

	return nil
}

func updateChannels(path string, payload channelPayload) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config failed: %w", err)
	}

	backup := path + ".bak"
	_ = os.WriteFile(backup, raw, 0644)

	clean := jsonc.ToJSON(raw)
	if !gjson.ValidBytes(clean) {
		return errors.New("config json invalid")
	}

	updated := clean
	if payload.TelegramToken != "" {
		updated, _ = sjson.SetBytes(updated, "channels.telegram.enabled", true)
		updated, _ = sjson.SetBytes(updated, "channels.telegram.botToken", payload.TelegramToken)
		if payload.TelegramAllow != "" {
			updated, _ = sjson.SetBytes(updated, "channels.telegram.allowFrom", splitCSV(payload.TelegramAllow))
		}
	}

	if payload.QQBotToken != "" {
		updated, _ = sjson.SetBytes(updated, "channels.qq.enabled", true)
		updated, _ = sjson.SetBytes(updated, "channels.qq.botToken", payload.QQBotToken)
		if payload.QQAppID != "" {
			updated, _ = sjson.SetBytes(updated, "channels.qq.appId", payload.QQAppID)
		}
		if payload.QQAppKey != "" {
			updated, _ = sjson.SetBytes(updated, "channels.qq.appKey", payload.QQAppKey)
		}
		if payload.QQAllow != "" {
			updated, _ = sjson.SetBytes(updated, "channels.qq.allowFrom", splitCSV(payload.QQAllow))
		}
	}

	if payload.DiscordToken != "" {
		updated, _ = sjson.SetBytes(updated, "channels.discord.enabled", true)
		updated, _ = sjson.SetBytes(updated, "channels.discord.token", payload.DiscordToken)
		if payload.DiscordGuild != "" {
			updated, _ = sjson.SetBytes(updated, "channels.discord.groupPolicy", "allowlist")
			updated, _ = sjson.SetBytes(updated, "channels.discord.guilds."+payload.DiscordGuild+".requireMention", true)
			if payload.DiscordUser != "" {
				updated, _ = sjson.SetBytes(updated, "channels.discord.guilds."+payload.DiscordGuild+".users", []string{payload.DiscordUser})
			}
		}
	}

	if err := os.WriteFile(path, updated, 0644); err != nil {
		return fmt.Errorf("write config failed: %w", err)
	}

	if out := runCmd(gOpenclawBin, withProfile(gProfile, "config", "validate")...); strings.Contains(strings.ToLower(out), "error") {
		return fmt.Errorf("config validate failed: %s", out)
	}

	return nil
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinArray(res gjson.Result) string {
	if !res.Exists() {
		return ""
	}
	if res.IsArray() {
		vals := []string{}
		for _, r := range res.Array() {
			vals = append(vals, r.String())
		}
		return strings.Join(vals, ",")
	}
	return res.String()
}

func firstGuild(clean []byte) (string, string) {
	guilds := gjson.GetBytes(clean, "channels.discord.guilds").Map()
	for k, v := range guilds {
		users := joinArray(v.Get("users"))
		return k, users
	}
	return "", ""
}

func runCmd(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%s\n(err: %v)", string(out), err)
	}
	return string(out)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func withProfile(profile string, args ...string) []string {
	if strings.TrimSpace(profile) == "" {
		return args
	}
	return append([]string{"--profile", profile}, args...)
}

func escapeJSON(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r")
	return replacer.Replace(s)
}

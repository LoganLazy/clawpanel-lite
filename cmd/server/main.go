package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

type chatTestResp struct {
	Output string `json:"output"`
	LatencyMs int64 `json:"latencyMs"`
	Ok bool `json:"ok"`
}

type chatPayload struct {
	Message string `json:"message"`
	Model   string `json:"model"`
}

type statusResp struct {
	OpenClaw string `json:"openclaw"`
	Gateway  string `json:"gateway"`
}

type installPayload struct {
	Version string `json:"version"`
}

type initStatusResp struct {
	OpenClawInstalled bool   `json:"openclawInstalled"`
	ConfigFound       bool   `json:"configFound"`
	ConfigPath        string `json:"configPath"`
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

type cronAction struct {
	ID string `json:"id"`
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

type rawPayload struct {
	Raw string `json:"raw"`
}

type skillManagePayload struct {
	Name   string `json:"name"`
	GitURL string `json:"gitUrl"`
	Target string `json:"target"` // managed|workspace
}

type commonSkill struct {
	Name   string `json:"name"`
	GitURL string `json:"gitUrl"`
	Target string `json:"target"`
}

type commonInstallPayload struct {
	Name string `json:"name"`
}

type skillEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Target string `json:"target"`
	Git    string `json:"git"`
}

type logQuery struct {
	Query string `json:"query"`
}

type backupPayload struct {
	Name string `json:"name"`
}

type restorePayload struct {
	Name string `json:"name"`
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	port := envOr("CLAWPANEL_PORT", defaultPort)
	user := envOr("CLAWPANEL_USER", defaultUser)
	pass := envOr("CLAWPANEL_PASS", defaultPass)
	bin := envOr("CLAWPANEL_OPENCLAW_BIN", "openclaw")
	installScript := envOr("CLAWPANEL_INSTALL_SCRIPT", "https://openclaw.ai/install.sh")
	installScriptCN := envOr("CLAWPANEL_INSTALL_SCRIPT_CN", "https://clawd.org.cn/install.sh")
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

	mux.HandleFunc("/api/channels/raw/get", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
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
		raw := gjson.GetBytes(clean, "channels").Raw
		if raw == "" {
			raw = "{}"
		}
		writeJSON(w, map[string]string{"raw": raw})
	}))

	mux.HandleFunc("/api/channels/raw/set", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
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
		var payload rawPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := updateChannelsRaw(path, payload.Raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/templates", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		templates := []apiTemplate{
			{Name: "OpenAI GPT-4o", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
			{Name: "OpenAI GPT-4.1", BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1"},
			{Name: "Anthropic Claude", BaseURL: "https://api.anthropic.com", Model: "claude-3-7-sonnet"},
			{Name: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Model: "gemini-1.5-pro"},
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

	mux.HandleFunc("/api/logs/search", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload logQuery
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		out := runCmd(bin, withProfile(profile, "logs", "--limit", "400", "--plain")...)
		if payload.Query != "" {
			filtered := []string{}
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(strings.ToLower(line), strings.ToLower(payload.Query)) {
					filtered = append(filtered, line)
				}
			}
			out = strings.Join(filtered, "\n")
		}
		writeJSON(w, logsResp{Output: out})
	}))

	mux.HandleFunc("/api/logs/download", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "logs", "--limit", "800", "--plain")...)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Disposition", "attachment; filename=clawpanel-logs.txt")
		_, _ = w.Write([]byte(out))
	}))

	mux.HandleFunc("/api/gateway/restart", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out := runCmd(bin, withProfile(profile, "gateway", "restart")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/init/status", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		path, found := detectConfigPath()
		writeJSON(w, initStatusResp{OpenClawInstalled: openclawExists(), ConfigFound: found, ConfigPath: path})
	}))

	mux.HandleFunc("/api/init/config", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload configPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(payload.Model) == "" {
			payload.Model = "openai/gpt-4o-mini"
		}
		if strings.TrimSpace(payload.BaseURL) == "" {
			payload.BaseURL = "https://api.openai.com/v1"
		}
		path, _ := detectConfigPath()
		if err := ensureConfigFile(path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := updateConfigFile(path, payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/openclaw/version", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "--version")...)
		writeJSON(w, map[string]string{"version": strings.TrimSpace(out)})
	}))

	mux.HandleFunc("/api/openclaw/update/check", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		current := strings.TrimSpace(runCmd(bin, withProfile(profile, "--version")...))
		latest := strings.TrimSpace(runCmd("bash", "-lc", "npm view openclaw version"))
		if strings.Contains(current, "(") {
			// keep current as-is
		}
		hasUpdate := false
		if latest != "" && current != "" && !strings.Contains(current, latest) {
			hasUpdate = true
		}
		writeJSON(w, map[string]interface{}{"current": current, "latest": latest, "hasUpdate": hasUpdate})
	}))

	mux.HandleFunc("/api/openclaw/update", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload installPayload
		_ = json.Unmarshal(body, &payload)
		url := installScript
		if payload.Version == "cn" {
			url = installScriptCN
		}
		out := runCmd("bash", "-lc", "HOME=/root curl -fsSL "+url+" | bash -s -- --no-onboard")
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/chat/test", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload chatPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = "你好"
		}
		start := time.Now()
		args := withProfile(profile, "agent", "--message", msg, "--json")
		if strings.TrimSpace(payload.Model) != "" {
			args = append(args, "--model", payload.Model)
		}
		out := runCmd(bin, args...)
		latency := time.Since(start).Milliseconds()
		ok := !strings.Contains(out, "(err:")
		writeJSON(w, chatTestResp{Output: out, LatencyMs: latency, Ok: ok})
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
			at := time.Now().Add(time.Duration(payload.Minutes) * time.Minute).Format(time.RFC3339)
			sched = "{\"kind\":\"at\",\"at\":\"" + at + "\"}"
		}
		job := "{\"name\":\"" + payload.JobName + "\",\"schedule\":" + sched + ",\"payload\":{\"kind\":\"systemEvent\",\"text\":\"" + escapeJSON(payload.Text) + "\"},\"sessionTarget\":\"main\"}"

		out := runCmd(bin, withProfile(profile, "cron", "add", "--job", job)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/cron/list", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		out := runCmd(bin, withProfile(profile, "cron", "list")...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/cron/runs", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		out := runCmd(bin, withProfile(profile, "cron", "runs", id)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	mux.HandleFunc("/api/cron/rm", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload cronAction
		if err := json.Unmarshal(body, &payload); err != nil || payload.ID == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		out := runCmd(bin, withProfile(profile, "cron", "rm", payload.ID)...)
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
			http.Error(w, "invalid json", http.StatusBadRequest)
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

	mux.HandleFunc("/api/skills/local/list", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		entries := []skillEntry{}
		entries = append(entries, listLocalSkills(skillsDirManaged(), "managed")...)
		entries = append(entries, listLocalSkills(skillsDirWorkspace(), "workspace")...)
		writeJSON(w, entries)
	}))

	mux.HandleFunc("/api/skills/local/install", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload skillManagePayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := installSkill(payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/skills/local/update", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload skillManagePayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := updateSkill(payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/skills/local/remove", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload skillManagePayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := removeSkill(payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/backups/list", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		items := listBackups()
		writeJSON(w, items)
	}))
	mux.HandleFunc("/api/skills/common/list", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		items := loadCommonSkills()
		writeJSON(w, items)
	}))

	mux.HandleFunc("/api/skills/common/save", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		items := []commonSkill{}
		if err := json.Unmarshal(body, &items); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := saveCommonSkills(items); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/skills/common/install", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload commonInstallPayload
		if err := json.Unmarshal(body, &payload); err != nil || payload.Name == "" {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		skill, ok := findCommonSkill(payload.Name)
		if !ok {
			http.Error(w, "skill not found", http.StatusNotFound)
			return
		}
		if err := installSkill(skillManagePayload{Name: skill.Name, GitURL: skill.GitURL, Target: skill.Target}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))


	mux.HandleFunc("/api/backups/create", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload backupPayload
		_ = json.Unmarshal(body, &payload)
		name := payload.Name
		if strings.TrimSpace(name) == "" {
			name = time.Now().Format("20060102-150405")
		}
		path, found := detectConfigPath()
		if !found {
			http.Error(w, "config not found", http.StatusNotFound)
			return
		}
		if err := createBackup(path, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("/api/backups/restore", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload restorePayload
		if err := json.Unmarshal(body, &payload); err != nil || payload.Name == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		path, found := detectConfigPath()
		if !found {
			http.Error(w, "config not found", http.StatusNotFound)
			return
		}
		if err := restoreBackup(path, payload.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
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
		out := runCmd(bin, withProfile(profile, "browser", "screenshot", path)...)
		writeJSON(w, map[string]string{"output": out})
	}))

	fs, _ := fs.Sub(webFS, "web")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(fs))))

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

	if openclawExists() {
		if out := runCmd(gOpenclawBin, withProfile(gProfile, "config", "validate")...); strings.Contains(strings.ToLower(out), "error") {
			return fmt.Errorf("config validate failed: %s", out)
		}
	}

	return nil
}
	path := expand(pathRaw)
	if fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	minimal := []byte("{}")
	return os.WriteFile(path, minimal, 0600)
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

	if openclawExists() {
		if out := runCmd(gOpenclawBin, withProfile(gProfile, "config", "validate")...); strings.Contains(strings.ToLower(out), "error") {
			return fmt.Errorf("config validate failed: %s", out)
		}
	}

	return nil
}

func updateChannelsRaw(path string, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("raw channels is empty")
	}

	parsed := jsonc.ToJSON([]byte(raw))
	if !gjson.ValidBytes(parsed) {
		return errors.New("channels json invalid")
	}

	cfg, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config failed: %w", err)
	}

	backup := path + ".bak"
	_ = os.WriteFile(backup, cfg, 0644)

	clean := jsonc.ToJSON(cfg)
	updated, err := sjson.SetRawBytes(clean, "channels", parsed)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, updated, 0644); err != nil {
		return fmt.Errorf("write config failed: %w", err)
	}

	if openclawExists() {
		if out := runCmd(gOpenclawBin, withProfile(gProfile, "config", "validate")...); strings.Contains(strings.ToLower(out), "error") {
			return fmt.Errorf("config validate failed: %s", out)
		}
	}

	return nil
}

func skillsDirManaged() string {
	if v := os.Getenv("CLAWPANEL_SKILLS_DIR"); v != "" {
		return expand(v)
	}
	return expand("~/.openclaw/skills")
}

func skillsDirWorkspace() string {
	ws := envOr("CLAWPANEL_WORKSPACE", "~/.openclaw/workspace")
	return filepath.Join(expand(ws), "skills")
}

func dataDir() string {
	if v := os.Getenv("CLAWPANEL_DATA_DIR"); v != "" {
		return expand(v)
	}
	return "/opt/clawpanel-lite/data"
}

func commonSkillsPath() string {
	return filepath.Join(dataDir(), "common-skills.json")
}

func loadCommonSkills() []commonSkill {
	path := commonSkillsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return []commonSkill{}
	}
	items := []commonSkill{}
	if err := json.Unmarshal(data, &items); err != nil {
		return []commonSkill{}
	}
	return items
}

func saveCommonSkills(items []commonSkill) error {
	if err := os.MkdirAll(dataDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(commonSkillsPath(), data, 0644)
}

func findCommonSkill(name string) (commonSkill, bool) {
	for _, s := range loadCommonSkills() {
		if s.Name == name {
			return s, true
		}
	}
	return commonSkill{}, false
}

func listLocalSkills(base string, target string) []skillEntry {
	entries := []skillEntry{}
	st, err := os.Stat(base)
	if err != nil || !st.IsDir() {
		return entries
	}
	items, _ := os.ReadDir(base)
	for _, it := range items {
		if !it.IsDir() || strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(base, it.Name())
		git := runCmd("git", "-C", path, "remote", "get-url", "origin")
		entries = append(entries, skillEntry{Name: it.Name(), Path: path, Target: target, Git: strings.TrimSpace(git)})
	}
	return entries
}

func installSkill(payload skillManagePayload) error {
	if strings.TrimSpace(payload.GitURL) == "" {
		return errors.New("gitUrl is required")
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = inferNameFromGit(payload.GitURL)
	}
	if !validName(name) {
		return errors.New("invalid name")
	}
	base := skillsDirManaged()
	if strings.ToLower(payload.Target) == "workspace" {
		base = skillsDirWorkspace()
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return err
	}
	dest := filepath.Join(base, name)
	if fileExists(dest) {
		return errors.New("skill already exists")
	}
	out := runCmd("git", "clone", payload.GitURL, dest)
	if strings.Contains(strings.ToLower(out), "error") {
		return errors.New(out)
	}
	return nil
}

func updateSkill(payload skillManagePayload) error {
	name := strings.TrimSpace(payload.Name)
	if !validName(name) {
		return errors.New("invalid name")
	}
	base := skillsDirManaged()
	if strings.ToLower(payload.Target) == "workspace" {
		base = skillsDirWorkspace()
	}
	path := filepath.Join(base, name)
	if !fileExists(path) {
		return errors.New("skill not found")
	}
	out := runCmd("git", "-C", path, "pull")
	if strings.Contains(strings.ToLower(out), "error") {
		return errors.New(out)
	}
	return nil
}

func removeSkill(payload skillManagePayload) error {
	name := strings.TrimSpace(payload.Name)
	if !validName(name) {
		return errors.New("invalid name")
	}
	base := skillsDirManaged()
	if strings.ToLower(payload.Target) == "workspace" {
		base = skillsDirWorkspace()
	}
	path := filepath.Join(base, name)
	if !fileExists(path) {
		return errors.New("skill not found")
	}
	trash := filepath.Join(base, ".trash")
	if err := os.MkdirAll(trash, 0755); err != nil {
		return err
	}
	newName := fmt.Sprintf("%s-%d", name, time.Now().Unix())
	return os.Rename(path, filepath.Join(trash, newName))
}

func inferNameFromGit(url string) string {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	name := parts[len(parts)-1]
	name = strings.TrimSuffix(name, ".git")
	return name
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
			return false
		}
	}
	return true
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

func listBackups() []string {
	path, found := detectConfigPath()
	if !found {
		return []string{}
	}
	backupDir := filepath.Dir(path) + "/backups"
	items, err := os.ReadDir(backupDir)
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, it := range items {
		if it.IsDir() {
			continue
		}
		if strings.HasSuffix(it.Name(), ".json") || strings.HasSuffix(it.Name(), ".json5") {
			out = append(out, it.Name())
		}
	}
	return out
}

func createBackup(configPath string, name string) error {
	backupDir := filepath.Dir(configPath) + "/backups"
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return err
	}
	src, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".json5") {
		name = name + ".json"
	}
	dest := filepath.Join(backupDir, name)
	return os.WriteFile(dest, src, 0644)
}

func restoreBackup(configPath string, name string) error {
	backupDir := filepath.Dir(configPath) + "/backups"
	path := filepath.Join(backupDir, name)
	if !fileExists(path) {
		return errors.New("backup not found")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	backup := configPath + ".bak"
	_ = os.WriteFile(backup, src, 0644)
	if err := os.WriteFile(configPath, src, 0644); err != nil {
		return err
	}
	if out := runCmd(gOpenclawBin, withProfile(gProfile, "config", "validate")...); strings.Contains(strings.ToLower(out), "error") {
		return fmt.Errorf("config validate failed: %s", out)
	}
	return nil
}

func openclawExists() bool {
	_, err := exec.LookPath(gOpenclawBin)
	return err == nil
}

func runCmd(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	env := os.Environ()
	hasHome := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			hasHome = true
			break
		}
	}
	if !hasHome {
		env = append(env, "HOME=/root")
	}
	cmd.Env = env
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

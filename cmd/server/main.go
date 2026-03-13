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

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	port := envOr("CLAWPANEL_PORT", defaultPort)
	user := envOr("CLAWPANEL_USER", defaultUser)
	pass := envOr("CLAWPANEL_PASS", defaultPass)
	bin := envOr("CLAWPANEL_OPENCLAW_BIN", "openclaw")
	installScript := envOr("CLAWPANEL_INSTALL_SCRIPT", "https://openclaw.ai/install.sh")
	profile := envOr("CLAWPANEL_PROFILE", "")

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

	mux.HandleFunc("/api/status", withAuth(sc, func(w http.ResponseWriter, r *http.Request) {
		openclaw := runCmd(bin, withProfile(profile, "status")...)
		gateway := runCmd(bin, withProfile(profile, "gateway", "status")...)
		writeJSON(w, statusResp{OpenClaw: openclaw, Gateway: gateway})
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

	if out := runCmd("openclaw", "config", "validate"); strings.Contains(strings.ToLower(out), "error") {
		return fmt.Errorf("config validate failed: %s", out)
	}

	return nil
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

# ClawPanel Lite (Custom)

轻量 OpenClaw 管理面板（Web 版），用于本机可视化配置与控制。

## 功能
- 单用户登录
- 配置：model / apiKey / baseUrl（自定义国产 API）
- 国产 API 模板（通义/智谱/百川/讯飞/DeepSeek）
- 自动探测 OpenClaw 配置文件路径
- 修改前自动备份 + 校验
- 状态查看 + 重启 Gateway
- 日志查看（最近 200 行）
- 系统概况（uname/uptime/disk/mem）
- 简单聊天测试（调用 `openclaw agent`）
- 一键安装 OpenClaw（页面按钮）
- Cron 任务创建（简单运维）
- 渠道配置（Telegram / QQ / Discord）
- 渠道原始配置（JSON 编辑）
- 配对码管理（list/approve）
- 技能列表/检测/info
- 技能管理（Git 安装/更新/移除）
- 浏览器管理（状态/启动/停止/打开URL/截图）
- Chrome 插件安装/路径查看

## 运行
```bash
./clawpanel-lite
```
默认端口：1450
默认账号：admin
默认密码：claw520

## 配置文件探测顺序
1. `~/.openclaw/openclaw.json`
2. `/etc/openclaw/openclaw.json`

可通过环境变量覆盖：
- `CLAWPANEL_CONFIG_PATH=/path/to/openclaw.json`
- `CLAWPANEL_OPENCLAW_BIN=/path/to/openclaw`
- `CLAWPANEL_INSTALL_SCRIPT=https://openclaw.ai/install.sh`
- `CLAWPANEL_PROFILE=dev` （使用 `openclaw --profile dev`）
- `CLAWPANEL_SKILLS_DIR=/custom/skills`
- `CLAWPANEL_WORKSPACE=/custom/workspace`

## 构建
```bash
go build -o clawpanel-lite ./cmd/server
```

## 一键安装（服务器）
```bash
curl -fsSL https://raw.githubusercontent.com/LoganLazy/clawpanel-lite/main/scripts/install.sh | bash
```

如需连带安装 OpenClaw：
```bash
curl -fsSL https://raw.githubusercontent.com/LoganLazy/clawpanel-lite/main/scripts/install.sh | bash -s -- --install-openclaw
```

## 备注
- 适配 OpenClaw JSON5 配置（支持注释/尾逗号）。
- 通过 `openclaw config validate` 校验配置。
- 通过 `openclaw gateway restart` 重启网关。

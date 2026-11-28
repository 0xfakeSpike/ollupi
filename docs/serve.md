# 把 ollupi 跑成一个服务器

`ollupi serve` 在一台机器上跑一个完整的 ollupi 部署：网关、会话调度，以及这台机器作为命令运行的沙箱。桌面端把它的地址和 token 注册成一个服务器，就能连过去；一个服务器可以被多台桌面端使用。

桌面端自带的本地后端和 `ollupi serve` 是同一套装配（引擎的 `server` 包），差别只在传入的选项：桌面端多接入了内置浏览器、随包技能和 `scratch` 目录，独立服务器则多了 `--allow-host` 与 TLS。

## 启动

```bash
go install github.com/0xfakeSpike/ollupi/cmd/ollupi@latest

export OLLUPI_GATEWAY_TOKEN=$(openssl rand -hex 32)
export OLLUPI_OPERATOR_TOKEN=$(openssl rand -hex 32)
ollupi serve --home ~/.ollupi --listen 127.0.0.1:7777
```

启动后打印：

```
ollupi serve
  address   ws://127.0.0.1:7777/
  profiles  admin
  tokens    a gateway token, a dot and a profile, e.g. <secret>.admin
```

token 不打印：它就是启动时给的 `OLLUPI_GATEWAY_TOKEN` 或 `OLLUPI_OPERATOR_TOKEN`。一个连接的 token 是「密钥.身份」：点前面是密钥，点后面是库里已有的身份；密钥里不能有点。

- 网关 token：以普通用户身份连接。能用模型、会话、工作区，不能改所有身份共用的模型目录，也不能新建身份。
- 运营者 token：同时是运营者，可以改模型、新建和命名身份。只给完全信任的桌面端。

服务器第一次启动、库里还没有身份时，会初始化一个 `admin` 身份（显示名 Admin）。用运营者 token 连 `<operator-token>.admin` 就是管理员。注意管理员权限来自运营者 token，不是这个身份：同一个 `admin` 用普通 token 连，就只是普通用户。之后别的身份由运营者在桌面端的「设置 → 身份」里新建，id 是新的 uuid；token 只认库里已有的身份。

## 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--home` | `$OLLUPI_HOME` 或 `~/.ollupi` | 数据库、附件、每个身份的文件夹 |
| `--listen` | `127.0.0.1:7777` | 网关监听的地址。默认只监听本机；要直接对外就换成 `0.0.0.0:7777` 或具体的网卡地址，并用 `--allow-host` 放行访问它的 Host |
| `--allow-host` | 空 | 除 `localhost`、`127.0.0.1`、`::1` 之外，还接受哪些 Host 名（不带端口），逗号分隔；反向代理不改写 Host 时用 |
| `--allow-origin` | 空 | 除本机之外，还接受哪些 WebSocket Origin，逗号分隔。桌面端的页面 Origin 是 `app://ollupi`，所以填 `ollupi` |
| `--tls-cert` / `--tls-key` | 空 | PEM 证书和私钥；两个都给时直接服务 `wss://`，否则是 `ws://` |
| `--gateway-token` / `--operator-token` | 读同名环境变量 | 也可以从参数给；优先用环境变量，命令行参数在进程列表里能被别的用户看到 |

## 让桌面端连上

**新服务器用 `admin` 连**：库初始化时建好了 `admin`，桌面端的 token 填 `<operator-token>.admin`，就是管理员。之后的桌面端只能用服务器上已有的身份，或者由运营者在「设置 → 身份」里新建。

**本机或 SSH 隧道**：默认只监听 `127.0.0.1:7777`。桌面端的服务器地址只接受 `wss://`，或者回环地址上的 `ws://`；跨机器直连用 SSH 端口转发（`ssh -L 7777:127.0.0.1:7777 server`）后填 `ws://127.0.0.1:7777`，或者配 TLS 直接填 `wss://`。

**Cloudflare Tunnel**：见 ollupi-desktop 仓库的 [docs/remote-mac-mini.md](https://github.com/0xfakeSpike/ollupi-desktop/blob/main/docs/remote-mac-mini.md)。cloudflared 用 `originRequest.httpHostHeader: 127.0.0.1` 改写 Host，就不需要 `--allow-host`；不改写时把域名填进 `--allow-host`。

**直接 TLS**：

```bash
ollupi serve --listen 0.0.0.0:7777 \
  --allow-host ollupi.example.com --allow-origin ollupi \
  --tls-cert /path/cert.pem --tls-key /path/key.pem
```

桌面端的地址填 `wss://ollupi.example.com:7777`（443 端口可省略），网关 token 填 `<secret>.<profile>`，Cloudflare Access 两项留空。

## Cloudflare 沙箱

`ollupi serve` 可以把 Cloudflare 的 sandbox 作为一种沙箱提供出来；桌面端建环境时就能选它：

```bash
export SANDBOX_API_KEY=...            # 只从环境变量读，别放参数
ollupi serve ... \
  --cloudflare-sandbox https://<bridge> \
  --cloudflare-root /workspace \
  --default-sandbox cloudflare         # 可选：新身份的第一个环境就用它
```

- 一个环境一个 Cloudflare sandbox；它的 id 存在环境的 state 里，服务重启后按 id 找回，删除环境会删掉它。
- 命令走 `/exec`（SSE，流式）；文件走 `/file/{path}` 的 GET/PUT（路径在 `/workspace` 内、不带前导斜杠）；`stat`、列目录、建目录、移动是在沙箱里跑 `stat`、`find`、`mkdir`、`mv`，镜像里要有 coreutils 和 findutils。
- 终端走 `/pty`（WebSocket：二进制帧是输入/输出，文本帧是 `resize`/`exit`）；新环境默认在 `/workspace`，`--cloudflare-root` 可改。
- 完全权限的命令可以跑（容器就是边界，`CanEscalate` 为 true）；MCP server 需要裸 stdin/stdout 的进程，PTY 不是合适的通道，仍不支持。
- bridge 还有 `/running`（探活）、`/session`（工作目录/环境变量作用域）、`/persist` 与 `/hydrate`（工作区 tar）。workspace 持久化可以先靠后两个。
- Cloudflare 的容器磁盘是临时的：sandbox 一旦没了，里面的工作区文件也就没了，重要文件不要只放在沙箱里。

## 模型

新服务器里没有模型：模型服务和密钥属于部署的数据库，由运营者连上来后在「设置 → 模型」里添加，保存即生效，不用重启。在那之前新会话发不出去（没有可用的模型）。

## 数据与限制

- `--home` 下：`data/` 是 SQLite 数据库，`users/<身份>/<环境>/` 是每个身份的工作区文件夹，`attachments/` 是附件。
- 这台机器就是沙箱：命令以运行 `ollupi serve` 的用户身份执行，受 Seatbelt 限制，读不到 `home/data`，也拿不到后端的 `OLLUPI_` 环境变量（两个 token 在里面）。能连上这个服务器、让它跑命令的人，等于能在它所在的机器上开 shell，所以别把运营者 token 给别人。
- 不要用 root 跑。
- `home/data` 里是模型密钥和所有身份的数据：备份它，别把它放进任何工作区。
- 服务器不会自己重启；用 launchd 托管它，`SIGTERM` 会让它写完当前状态再退出。命令沙箱目前是 macOS 的 Seatbelt，别的平台要等对应的沙箱实现。

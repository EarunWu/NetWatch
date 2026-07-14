# NetWatch service（桌面版 sidecar）

轻量、仅标准库的本地 TCP 与代理节点质量监测服务。桌面版由 `NetWatch.exe` 启动它作为 `NetWatch.Service.exe` sidecar；服务固定监听 `127.0.0.1:9288`，不会暴露到局域网。节点模式经本地SOCKS5和当前代理节点建立Google TLS连接，可选继续验证HTTP 204，不自行实现具体代理协议。

## 运行

普通用户不需要直接启动本服务：双击 `NetWatch.exe` 后，Tauri 外壳负责启动、检测就绪和关闭 sidecar。独立开发、API 调试或旧浏览器兼容模式仍可手动运行：

```powershell
go run .
go run . --data-dir C:\path\to\data
go build -trimpath -ldflags "-s -w" -o netwatch-service.exe .
# 无控制台后台版本（日志仍写入 netwatch.log）
go build -trimpath -ldflags "-s -w -H=windowsgui" -o netwatch-service.exe .
```

桌面外壳使用 `--managed` 参数。托管模式在监听成功后向 stdout 写入一行包含 `type/version/protocol/instance/address/url` 的 ready JSON；收到 stdin 中的 `shutdown` 或父进程关闭管道造成的 EOF 时，会在最多5秒内正常停止 HTTP 服务和探测任务。这个协议供 Tauri 使用，不建议普通用户手工操作。

首次启动且 `targets.json` 不存在时，会创建两个可删除的直接TCP示例目标。配置以原子替换方式写入 `%LOCALAPPDATA%\NetWatch\targets.json`；桌面化不会迁移或重置该目录，macOS 对应 `~/Library/Application Support/NetWatch`。version 1/2仍可读取，缺少`google_204_enabled`时按`false`处理，保存时写为version 3。启动、关闭和异常日志追加到同目录的 `netwatch.log`，不会记录高频sample。每个目标在内存中保留最近900个高精度样本，另以一分钟聚合桶保留最多12小时的图表历史；服务重启后历史重新积累。不启动外部命令，也不为每次探测创建新线程或进程。

Tauri 桌面版的 React 页面由桌面包携带，不从 sidecar 的静态站点加载。服务暂时保留 `service/web` 与 `go:embed`，用于 `npm run build:legacy` 生成旧浏览器兼容程序；旧模式下未知的 GET 页面路径会回退到 `index.html`，未知 `/api/*` 路径仍返回 JSON 404。

## API

- `GET /api/health`（包含 `status/version/protocol/instance/target_count/uptime_seconds/time`）
- `GET /api/targets`
- `POST /api/targets`
- `PUT /api/targets/{id}`
- `DELETE /api/targets/{id}`
- `GET /api/snapshot`
- `GET /api/events`（SSE；初连和 CRUD 变更时发送 `snapshot`，实时发送 `sample`，每 15 秒发送 `heartbeat`；最多 8 个客户端）

`GET /api/targets` 直接返回 Target 数组；POST 返回新 Target（201），PUT 返回更新后的 Target（200），DELETE 成功无响应体（204）。snapshot 和 SSE 数据结构固定为：

```json
{
  "generated_at": 1783872000000,
  "targets": [
    {
      "target": { "id": "...", "name": "Current Proxy Node", "kind": "proxy_google", "host": "www.google.com", "port": 443, "proxy_host": "127.0.0.1", "proxy_port": 10808, "google_204_enabled": false, "interval_ms": 5000, "timeout_ms": 8000, "enabled": true },
      "stats": { "current_ms": 303.7, "p95_ms": 320.1, "local_proxy_current_ms": 0.2, "tunnel_current_ms": 0.4, "remote_first_byte_current_ms": 166.9, "tls_current_ms": 303.7, "google_current_ms": null, "success_rate": 100, "estimated_loss_rate": 0, "tunnel_timeout_rate": 0, "google_timeout_rate": 0, "sample_count": 60, "last_status": "success" },
      "samples": [{ "target_id": "...", "ts": 1783872000000, "probe_kind": "proxy_google", "latency_ms": 303.7, "local_proxy_ms": 0.2, "tunnel_ms": 0.4, "remote_first_byte_ms": 166.9, "tls_ms": 303.7, "stage": "tls", "status": "success" }],
      "chart_samples": [{ "target_id": "...", "ts": 1783828800000, "probe_kind": "proxy_google", "latency_ms": 300.1, "tls_ms": 300.1, "stage": "tls", "status": "success", "bucket_ms": 60000 }],
      "chart_buckets": [{ "start_ms": 1783828800000, "duration_ms": 60000, "total_count": 12, "success_count": 11, "timeout_count": 1, "refused_count": 0, "tunnel_success_count": 12, "tunnel_timeout_count": 0, "latency_count": 11, "latency_sum": 3301.1, "latency_sum_squares": 990770.2, "tls_count": 11, "tls_sum": 3301.1, "tls_sum_squares": 990770.2, "latency_histogram": [{ "value_ms": 300.4, "count": 11 }] }]
    }
  ]
}
```

SSE 的 `snapshot` data 与上面完全相同；`sample` data 为 `{"sample": Sample, "stats": Stats}`；`heartbeat` data 为 `{"ts": epoch_ms}`。`chart_samples`仅用于绘图，包含早于900个高精度样本的分钟级历史：所有带有效测量值的已完成连接（包括`latency_spike`）共同计算桶内平均值，每分钟最多保留一个最终异常标记。`chart_buckets`为同一段历史提供可合并统计摘要；次数和二阶矩用于按时间范围准确计算比率与波动率，`latency_histogram`是相对误差不超过2%的稀疏对数分布，用于估算长范围P95。只输出完整结束于原始样本之前的分钟桶，避免重复计数。硬失败 Sample 通常省略 `latency_ms`，延迟尖峰则保留真实`latency_ms`/`tls_ms`。没有有效延迟测量时 Stats 的 `current_ms` / `p95_ms` 是 `null`。

直接TCP目标JSON：

```json
{
  "id": "创建时可省略",
  "name": "Primary API",
  "kind": "direct_tcp",
  "host": "api.example.com",
  "port": 443,
  "interval_ms": 5000,
  "timeout_ms": 2000,
  "enabled": true
}
```

节点探测目标JSON：

```json
{
  "name": "Current Proxy Node",
  "kind": "proxy_google",
  "host": "www.google.com",
  "port": 443,
  "proxy_host": "127.0.0.1",
  "proxy_port": 10808,
  "google_204_enabled": false,
  "interval_ms": 5000,
  "timeout_ms": 8000,
  "enabled": true
}
```

节点模式会强制TLS终点为`www.google.com:443`，代理地址只接受回环地址，探测间隔最低2秒。当前仅支持无需用户名和密码的SOCKS5入口；SOCKS5使用域名地址类型，把Google解析交给代理。`google_204_enabled`默认`false`，TLS握手和证书验证完成后即成功；设为`true`时才继续请求`/generate_204`，强制HTTP/1.1、禁用连接复用、添加随机查询参数并要求响应码严格等于204。

`tunnel_ms`是SOCKS确认时间，部分代理核心可能在本机提前确认而使其接近0；`remote_first_byte_ms`记录TLS握手期间首次收到远端字节的时间，是更可靠的tcping风格指标。默认成功样本以`tls_ms`作为`latency_ms`且`stage`为`tls`；启用204后才增加`google_ms`/`http_status`，并以`google_ms`作为主延迟。节点网络超时仍使用通用`status: timeout`并由`stage: socks | tls | http`区分；本地代理、SOCKS、TLS证书和HTTP异常保留独立状态，同时作为非成功探测计入丢包率。

`success_rate`、`timeout_rate`、`refused_rate` 和非空的 `estimated_loss_rate` 均为 `0..100` 的百分数。为兼容既有API，丢包率仍通过`estimated_loss_rate`字段返回，口径为`所有非 success 样本数 / 全部样本数`；没有样本时为`null`。每个目标使用最近30次有效测量的中位数作为滚动基准，至少10次后启用；本次延迟严格超过`max(基准 × 2, 基准 + 200ms)`时，最终样本记为`packet_loss`并增加`loss_reason: latency_spike`。直接TCP使用建连延迟，节点使用TLS完成延迟；尖峰保留`latency_ms`和各阶段真实测量，因此仍参与延迟图、P95和波动率。任意最终失败同样作为一次丢失，并保留具体状态，便于区分超时、拒绝、DNS、路由、本地代理、SOCKS、TLS和HTTP故障。域名解析使用独立超时并按系统顺序保留全部去重地址；TCP 在同一个总超时内顺序尝试，立即拒绝、无路由或其他错误时回退到后续地址。完成样本的`latency_ms`从第一次TCP Dial开始，包含地址fallback耗时，但不包含DNS耗时。

HTTP Host 只接受监听端口上的 loopback 名称或地址。跨源 API 精确允许 Windows Tauri 的 `http://tauri.localhost`、macOS Tauri 的 `tauri://localhost` 和 Vite 开发地址 `http://localhost:3000` / `http://127.0.0.1:3000`；旧浏览器页面只允许与当前 API 端口相同的 HTTP loopback Origin。无 Origin 的本机 CLI/原生请求可用。写接口只接受 `application/json`，拒绝未知字段、尾随 JSON 和超过 32 KiB 的请求体。

## v2rayN TUN 绕过

为最终固定的后台进程名 `NetWatch.Service.exe` 创建 `direct` 路由规则，network 设为 `tcp`，并放在最终代理规则之前。不要把规则写成 `NetWatch.exe` 或 `msedgewebview2.exe`：桌面外壳不执行探测。直接TCP探测由 sidecar 创建远端Socket；节点探测只连接本机SOCKS5，真正的远端出站由代理核心进程创建。域名目标的系统 DNS 请求未必归属于本进程；需要严格控制绕过路径时优先填写 IP。

## 桌面开发与构建

在项目根目录运行：

```powershell
npm install
npm run desktop:dev   # Tauri 桌面开发模式
npm run build         # 当前平台桌面安装包
npm run build:legacy  # 旧后台 EXE + 浏览器页面
```

Windows 安装器使用 `embedBootstrapper` WebView2 策略：内嵌小型引导程序，已有 Evergreen Runtime 时复用，缺少时安装器联网补齐。macOS 使用系统 WKWebView，正式桌面包需要在 Mac 上构建、签名和公证。桌面安装产物默认位于 `src-tauri/target/release/bundle/`。

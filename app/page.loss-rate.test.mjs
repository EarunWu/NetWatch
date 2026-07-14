import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";

function sample(status, latencyMs = status === "success" ? 80 : null) {
  return {
    target_id: "target-1",
    ts: Date.now(),
    probe_kind: "direct_tcp",
    latency_ms: latencyMs,
    local_proxy_ms: null,
    tunnel_ms: null,
    remote_first_byte_ms: null,
    tls_ms: null,
    google_ms: null,
    stage: "tcp",
    http_status: null,
    status,
    loss_reason: status === "packet_loss" ? "latency_spike" : "",
    message: "",
    bucket_ms: 0,
  };
}

function bucket(totalCount, successCount) {
  return {
    target_id: "target-1",
    start_ms: Date.now() - 60_000,
    duration_ms: 60_000,
    total_count: totalCount,
    success_count: successCount,
    timeout_count: 0,
    refused_count: 0,
    tunnel_success_count: 0,
    tunnel_timeout_count: 0,
    latency_count: 0,
    latency_sum: 0,
    latency_sum_squares: 0,
    tls_count: 0,
    tls_sum: 0,
    tls_sum_squares: 0,
    latency_histogram: [],
  };
}

test("丢包率把所有最终非成功结果计为丢失", async (context) => {
  const vite = await createServer({
    appType: "custom",
    logLevel: "silent",
    server: { middlewareMode: true },
  });
  context.after(() => vite.close());

  const { __testing } = await vite.ssrLoadModule("/app/page.tsx");
  const stats = __testing.deriveStats(
    [
      sample("success"),
      sample("timeout"),
      sample("local_proxy_timeout"),
      sample("packet_loss"),
    ],
    [],
    false,
  );

  assert.equal(stats.estimated_loss_rate, 75);
});

test("延迟尖峰计入丢包但保留真实延迟统计", async (context) => {
  const vite = await createServer({
    appType: "custom",
    logLevel: "silent",
    server: { middlewareMode: true },
  });
  context.after(() => vite.close());

  const { __testing } = await vite.ssrLoadModule("/app/page.tsx");
  const stats = __testing.deriveStats(
    [sample("success", 100), sample("packet_loss", 500)],
    [],
    false,
  );

  assert.equal(stats.estimated_loss_rate, 50);
  assert.equal(stats.success_rate, 50);
  assert.equal(stats.current_ms, 500);
  assert.equal(stats.average_ms, 300);
  assert.equal(stats.p95_ms, 500);
  assert.ok(Math.abs(stats.volatility_rate - 66.66666666666666) < 1e-9);
});

test("旧版分钟桶可用总数减成功数精确计算丢包率", async (context) => {
  const vite = await createServer({
    appType: "custom",
    logLevel: "silent",
    server: { middlewareMode: true },
  });
  context.after(() => vite.close());

  const { __testing } = await vite.ssrLoadModule("/app/page.tsx");
  const stats = __testing.deriveStats([], [bucket(10, 7)], false);

  assert.equal(stats.estimated_loss_rate, 30);
});

test("TCP 波动率同时支持实时样本和分钟摘要", async (context) => {
  const vite = await createServer({
    appType: "custom",
    logLevel: "silent",
    server: { middlewareMode: true },
  });
  context.after(() => vite.close());

  const { __testing } = await vite.ssrLoadModule("/app/page.tsx");
  const liveStats = __testing.deriveStats(
    [sample("success", 100), sample("timeout"), sample("success", 300)],
    [],
    false,
  );
  const minuteBucket = bucket(2, 2);
  minuteBucket.latency_count = 2;
  minuteBucket.latency_sum = 400;
  minuteBucket.latency_sum_squares = 100_000;
  const historyStats = __testing.deriveStats([], [minuteBucket], false);

  assert.equal(liveStats.volatility_rate, 50);
  assert.equal(historyStats.volatility_rate, 50);
  assert.equal(liveStats.average_ms, 200);
  assert.equal(historyStats.average_ms, 200);
  assert.equal(
    __testing.deriveStats([sample("success", 100)], [], false)
      .volatility_rate,
    null,
  );
});

test("折线图区分延迟尖峰与最终失败事件", async (context) => {
  const vite = await createServer({
    appType: "custom",
    logLevel: "silent",
    server: { middlewareMode: true },
  });
  context.after(() => vite.close());

  const { __testing } = await vite.ssrLoadModule("/app/page.tsx");
  const targets = [
    {
      id: "target-1",
      name: "测试目标",
      kind: "direct_tcp",
    },
  ];
  const events = __testing.buildLossEvents(
    targets,
    {
      "target-1": [
        sample("success"),
        sample("timeout"),
        sample("local_proxy_timeout"),
        sample("packet_loss", 500),
      ],
    },
    {},
  );

  assert.equal(events.length, 3);
  assert.equal(events[0].kind, "failure");
  assert.equal(events[0].value, null);
  assert.equal(events.at(-1).lossReason, "latency_spike");
  assert.equal(events.at(-1).kind, "estimated");
  assert.equal(events.at(-1).value, 500);
  assert.equal(
    __testing.lossStatusLabel("packet_loss", events.at(-1).lossReason),
    "延迟尖峰",
  );
});

test("直连与节点折线都保留实时尖峰测量", async (context) => {
  const vite = await createServer({
    appType: "custom",
    logLevel: "silent",
    server: { middlewareMode: true },
  });
  context.after(() => vite.close());

  const { __testing } = await vite.ssrLoadModule("/app/page.tsx");
  const direct = {
    id: "target-1",
    name: "直连",
    kind: "direct_tcp",
    interval_ms: 2000,
  };
  const node = {
    id: "node-1",
    name: "节点",
    kind: "proxy_google",
    interval_ms: 2000,
  };
  const directSpike = sample("packet_loss", 500);
  const nodeSpike = {
    ...sample("packet_loss", 520),
    target_id: "node-1",
    probe_kind: "proxy_google",
    stage: "tls",
    tls_ms: 500,
  };
  const series = __testing.buildLatencySeries(
    [direct, node],
    { "target-1": [directSpike], "node-1": [nodeSpike] },
    {},
  );

  assert.equal(series[0].points[0].value, 500);
  assert.equal(series[1].points[0].value, 500);
});

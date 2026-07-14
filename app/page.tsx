"use client";

import {
  FormEvent,
  KeyboardEvent,
  PointerEvent,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";

type ConnectionState = "connecting" | "live" | "offline";
type ProbeKind = "direct_tcp" | "proxy_google";

type Target = {
  id: string;
  name: string;
  kind: ProbeKind;
  host: string;
  port: number;
  proxy_host: string;
  proxy_port: number;
  google_204_enabled: boolean;
  interval_ms: number;
  timeout_ms: number;
  enabled: boolean;
};

type Sample = {
  target_id: string;
  ts: number;
  probe_kind: ProbeKind;
  latency_ms: number | null;
  local_proxy_ms: number | null;
  tunnel_ms: number | null;
  remote_first_byte_ms: number | null;
  tls_ms: number | null;
  google_ms: number | null;
  stage: string;
  http_status: number | null;
  status: string;
  loss_reason: string;
  message: string;
  bucket_ms: number;
};

type LatencyHistogramBin = {
  value_ms: number;
  count: number;
};

type ChartBucket = {
  target_id: string;
  start_ms: number;
  duration_ms: number;
  total_count: number;
  success_count: number;
  timeout_count: number;
  refused_count: number;
  tunnel_success_count: number;
  tunnel_timeout_count: number;
  latency_count: number;
  latency_sum: number;
  latency_sum_squares: number;
  tls_count: number;
  tls_sum: number;
  tls_sum_squares: number;
  latency_histogram: LatencyHistogramBin[];
};

type TargetStats = {
  current_ms: number | null;
  average_ms: number | null;
  p95_ms: number | null;
  p95_approximate: boolean;
  local_proxy_current_ms: number | null;
  tunnel_current_ms: number | null;
  remote_first_byte_current_ms: number | null;
  tls_current_ms: number | null;
  tls_average_ms: number | null;
  google_current_ms: number | null;
  success_rate: number | null;
  timeout_rate: number | null;
  refused_rate: number | null;
  estimated_loss_rate: number | null;
  tunnel_timeout_rate: number | null;
  google_timeout_rate: number | null;
  volatility_rate: number | null;
};

type ChartPoint = {
  ts: number;
  value: number | null;
  bucketMs: number;
};

type ChartSeries = {
  id: string;
  name: string;
  color: string;
  intervalMs: number;
  dash?: number[];
  points: ChartPoint[];
};

type LossEvent = {
  id: string;
  name: string;
  color: string;
  markerColor: string;
  kind: "estimated" | "failure";
  value: number | null;
  ts: number;
  stage: string;
  status: string;
  lossReason: string;
  bucketMs: number;
};

type TargetState = {
  tone: "good" | "warning" | "danger" | "muted";
  label: string;
  detail: string;
  kind:
    | "success"
    | "timeout"
    | "refused"
    | "dns"
    | "route"
    | "error"
    | "waiting"
    | "paused";
};

type TargetFormValues = {
  name: string;
  kind: ProbeKind;
  host: string;
  port: string;
  proxy_host: string;
  proxy_port: string;
  google_204_enabled: boolean;
  interval_ms: string;
  timeout_ms: string;
  enabled: boolean;
};

type TooltipState = {
  x: number;
  alignRight: boolean;
  ts: number;
  rows: Array<{
    id: string;
    name: string;
    color: string;
    value: number | null;
    text?: string;
    tone?: "estimated" | "failure";
  }>;
};

const DEFAULT_API_BASE = "http://127.0.0.1:9288";
const API_BASE = (
  import.meta.env.VITE_API_BASE_URL?.trim() || DEFAULT_API_BASE
).replace(/\/+$/, "");
const SERVICE_UNAVAILABLE_MESSAGE =
  "无法连接本地监测核心（127.0.0.1:9288）。服务可能仍在启动、正在重启，或连接被系统网络策略阻止。";
const VIEW_PREFERENCES_KEY = "netwatch.desktop-view.v1";

const SERIES_COLORS = [
  "#55a6ff",
  "#20d3a6",
  "#f7b84b",
  "#ad8cff",
  "#ff748c",
  "#65d36e",
  "#55d5e8",
  "#ff915a",
];

const RANGE_OPTIONS = [
  { label: "15 分钟", value: 900_000 },
  { label: "1 小时", value: 3_600_000 },
  { label: "12 小时", value: 43_200_000 },
];

const EMPTY_STATS: TargetStats = {
  current_ms: null,
  average_ms: null,
  p95_ms: null,
  p95_approximate: false,
  local_proxy_current_ms: null,
  tunnel_current_ms: null,
  remote_first_byte_current_ms: null,
  tls_current_ms: null,
  tls_average_ms: null,
  google_current_ms: null,
  success_rate: null,
  timeout_rate: null,
  refused_rate: null,
  estimated_loss_rate: null,
  tunnel_timeout_rate: null,
  google_timeout_rate: null,
  volatility_rate: null,
};

const DEFAULT_FORM: TargetFormValues = {
  name: "",
  kind: "direct_tcp",
  host: "",
  port: "443",
  proxy_host: "127.0.0.1",
  proxy_port: "10808",
  google_204_enabled: false,
  interval_ms: "2000",
  timeout_ms: "1500",
  enabled: true,
};

const GOOGLE_PROBE_HOST = "www.google.com";
const GOOGLE_PROBE_PORT = 443;
const GOOGLE_PROBE_PATH = "/generate_204";

const LOSS_RATE_EXPLANATION =
  "按体验层估算：每个目标使用最近 30 次有效测量的中位数作为滚动基准，积累至少 10 次后启用。当本次延迟超过 max（基准 × 2，基准 + 200ms）时，即使连接最终完成，也按一次延迟尖峰计入丢包；连接超时或其他最终失败同样计入。尖峰会保留真实延迟并显示在折线图上。该指标也可能受到排队、服务器负载或路由变化影响，不等同于物理链路的包级丢包率。";

function LossRateLabel({ rangeLabel }: { rangeLabel: string }) {
  const tooltipId = useId();

  return (
    <span className="metric-label-with-help">
      <span>丢包率</span>
      <span className="metric-help-wrap">
        <button
          className="metric-help"
          type="button"
          aria-label={`${rangeLabel}丢包率计算说明`}
          aria-describedby={tooltipId}
        >
          ?
        </button>
        <span className="metric-help-tooltip" id={tooltipId} role="tooltip">
          {LOSS_RATE_EXPLANATION}
        </span>
      </span>
    </span>
  );
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function readString(...values: unknown[]): string {
  const found = values.find(
    (value) => typeof value === "string" || typeof value === "number",
  );
  return found === undefined || found === null ? "" : String(found);
}

function readNumber(...values: unknown[]): number | null {
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (
      typeof value === "string" &&
      value.trim() !== "" &&
      Number.isFinite(Number(value))
    ) {
      return Number(value);
    }
  }
  return null;
}

function readBoolean(value: unknown, fallback = true): boolean {
  if (typeof value === "boolean") return value;
  if (typeof value === "number") return value !== 0;
  if (typeof value === "string") {
    if (["false", "0", "off", "disabled"].includes(value.toLowerCase())) {
      return false;
    }
    if (["true", "1", "on", "enabled"].includes(value.toLowerCase())) {
      return true;
    }
  }
  return fallback;
}

function toTimestamp(value: unknown): number {
  const numeric = readNumber(value);
  if (numeric !== null) {
    if (numeric < 10_000_000_000) return numeric * 1000;
    return numeric;
  }
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return Date.now();
}

function normalizeRate(value: unknown): number | null {
  const numeric = readNumber(value);
  if (numeric === null) return null;
  return Math.max(0, Math.min(100, numeric));
}

type ViewPreferences = {
  rangeMs: number;
  selectedTargetId: string | null;
  visibleTargets: Record<string, boolean>;
};

function loadViewPreferences(): ViewPreferences {
  const fallback: ViewPreferences = {
    rangeMs: RANGE_OPTIONS[0].value,
    selectedTargetId: null,
    visibleTargets: {},
  };
  try {
    const record = asRecord(
      JSON.parse(window.localStorage.getItem(VIEW_PREFERENCES_KEY) ?? "null"),
    );
    if (!record) return fallback;
    const range = readNumber(record.rangeMs);
    const visibleRecord = asRecord(record.visibleTargets);
    return {
      rangeMs: RANGE_OPTIONS.some((option) => option.value === range)
        ? (range as number)
        : fallback.rangeMs,
      selectedTargetId: readString(record.selectedTargetId).trim() || null,
      visibleTargets: visibleRecord
        ? Object.fromEntries(
            Object.entries(visibleRecord).filter(
              (entry): entry is [string, boolean] =>
                typeof entry[1] === "boolean",
            ),
          )
        : {},
    };
  } catch {
    return fallback;
  }
}

function saveViewPreferences(preferences: ViewPreferences) {
  try {
    window.localStorage.setItem(
      VIEW_PREFERENCES_KEY,
      JSON.stringify(preferences),
    );
  } catch {
    // The desktop WebView normally has persistent storage; an unavailable
    // store should not prevent monitoring from starting.
  }
}

function normalizeStatus(value: unknown): string {
  return readString(value, "error").trim().toLowerCase().replaceAll("-", "_");
}

function statusKind(status: string):
  | "success"
  | "timeout"
  | "refused"
  | "dns"
  | "route"
  | "error" {
  const value = status.toLowerCase();
  if (
    ["success", "ok", "up", "connected", "online"].some(
      (token) => value === token || value.includes(token),
    )
  ) {
    return "success";
  }
  if (value.includes("timeout") || value.includes("timed_out")) return "timeout";
  if (
    value.includes("refused") ||
    value.includes("reset") ||
    value.includes("reject")
  ) {
    return "refused";
  }
  if (
    value.includes("dns") ||
    value.includes("resolve") ||
    value.includes("name_not")
  ) {
    return "dns";
  }
  if (
    value.includes("route") ||
    value.includes("unreachable") ||
    value.includes("network")
  ) {
    return "route";
  }
  return "error";
}

function normalizeTarget(value: unknown, index = 0): Target | null {
  const record = asRecord(value);
  if (!record) return null;
  const host = readString(
    record.host,
    record.hostname,
    record.address,
    record.ip,
  ).trim();
  const port = readNumber(record.port, record.tcp_port) ?? 0;
  const kind =
    readString(record.kind, record.probe_kind).toLowerCase() === "proxy_google"
      ? "proxy_google"
      : "direct_tcp";
  const id = readString(
    record.id,
    record.target_id,
    record.targetId,
    host ? host + ":" + port : "target-" + index,
  );
  if (!host && !id) return null;
  return {
    id,
    name:
      readString(record.name, record.label, record.title).trim() ||
      (host ? host + ":" + port : "未命名目标"),
    kind,
    host,
    port,
    proxy_host: readString(record.proxy_host, record.proxyHost).trim(),
    proxy_port: readNumber(record.proxy_port, record.proxyPort) ?? 0,
    google_204_enabled: readBoolean(
      record.google_204_enabled ?? record.google204Enabled,
      false,
    ),
    interval_ms: Math.max(
      250,
      readNumber(record.interval_ms, record.interval, record.intervalMs) ?? 2000,
    ),
    timeout_ms: Math.max(
      100,
      readNumber(record.timeout_ms, record.timeout, record.timeoutMs) ?? 1500,
    ),
    enabled: readBoolean(
      record.enabled ?? record.active ?? record.is_enabled,
      true,
    ),
  };
}

function normalizeSample(value: unknown, fallbackTargetId = ""): Sample | null {
  const record = asRecord(value);
  if (!record) return null;
  const targetId = readString(
    record.target_id,
    record.targetId,
    record.monitor_id,
    fallbackTargetId,
  );
  if (!targetId) return null;
  const latency = readNumber(
    record.latency_ms,
    record.latency,
    record.duration_ms,
    record.rtt_ms,
  );
  const probeKind =
    readString(record.probe_kind, record.kind).toLowerCase() === "proxy_google"
      ? "proxy_google"
      : "direct_tcp";
  return {
    target_id: targetId,
    ts: toTimestamp(
      record.ts ?? record.timestamp ?? record.time ?? record.created_at,
    ),
    latency_ms: latency === null ? null : Math.max(0, latency),
    probe_kind: probeKind,
    local_proxy_ms: readNumber(record.local_proxy_ms),
    tunnel_ms: readNumber(record.tunnel_ms),
    remote_first_byte_ms: readNumber(record.remote_first_byte_ms),
    tls_ms: readNumber(record.tls_ms),
    google_ms: readNumber(record.google_ms),
    stage: readString(record.stage),
    http_status: readNumber(record.http_status),
    status: normalizeStatus(record.status ?? record.result ?? record.state),
    loss_reason: readString(record.loss_reason, record.lossReason)
      .trim()
      .toLowerCase(),
    message: readString(record.message, record.error, record.detail),
    bucket_ms: Math.max(0, readNumber(record.bucket_ms) ?? 0),
  };
}

function normalizeStats(value: unknown): TargetStats {
  const record = asRecord(value);
  if (!record) return { ...EMPTY_STATS };
  return {
    current_ms: readNumber(
      record.current_ms,
      record.current,
      record.latest_ms,
      record.latency_ms,
    ),
    average_ms: readNumber(record.average_ms, record.latency_average_ms),
    p95_ms: readNumber(record.p95_ms, record.p95, record.latency_p95),
    p95_approximate: readBoolean(record.p95_approximate, false),
    local_proxy_current_ms: readNumber(record.local_proxy_current_ms),
    tunnel_current_ms: readNumber(record.tunnel_current_ms),
    remote_first_byte_current_ms: readNumber(
      record.remote_first_byte_current_ms,
    ),
    tls_current_ms: readNumber(record.tls_current_ms),
    tls_average_ms: readNumber(record.tls_average_ms),
    google_current_ms: readNumber(record.google_current_ms),
    success_rate: normalizeRate(record.success_rate ?? record.successRate),
    timeout_rate: normalizeRate(record.timeout_rate ?? record.timeoutRate),
    refused_rate: normalizeRate(record.refused_rate ?? record.refusedRate),
    estimated_loss_rate: normalizeRate(
      record.estimated_loss_rate ??
        record.estimatedLossRate ??
        record.loss_rate,
    ),
    tunnel_timeout_rate: normalizeRate(record.tunnel_timeout_rate),
    google_timeout_rate: normalizeRate(record.google_timeout_rate),
    volatility_rate: null,
  };
}

function normalizeCount(value: unknown): number {
  return Math.max(0, Math.floor(readNumber(value) ?? 0));
}

function normalizeChartBucket(
  value: unknown,
  fallbackTargetId = "",
): ChartBucket | null {
  const record = asRecord(value);
  if (!record) return null;
  const targetId = readString(record.target_id, record.targetId, fallbackTargetId);
  const start = readNumber(record.start_ms);
  const duration = readNumber(record.duration_ms);
  if (!targetId || start === null || duration === null || duration <= 0) {
    return null;
  }
  const histogram = valuesFromCollection(record.latency_histogram)
    .map((entry): LatencyHistogramBin | null => {
      const bin = asRecord(entry);
      const valueMS = readNumber(bin?.value_ms);
      const count = normalizeCount(bin?.count);
      return valueMS !== null && valueMS >= 0 && count > 0
        ? { value_ms: valueMS, count }
        : null;
    })
    .filter((entry): entry is LatencyHistogramBin => entry !== null);
  return {
    target_id: targetId,
    start_ms: start,
    duration_ms: duration,
    total_count: normalizeCount(record.total_count),
    success_count: normalizeCount(record.success_count),
    timeout_count: normalizeCount(record.timeout_count),
    refused_count: normalizeCount(record.refused_count),
    tunnel_success_count: normalizeCount(record.tunnel_success_count),
    tunnel_timeout_count: normalizeCount(record.tunnel_timeout_count),
    latency_count: normalizeCount(record.latency_count),
    latency_sum: Math.max(0, readNumber(record.latency_sum) ?? 0),
    latency_sum_squares: Math.max(
      0,
      readNumber(record.latency_sum_squares) ?? 0,
    ),
    tls_count: normalizeCount(record.tls_count),
    tls_sum: Math.max(0, readNumber(record.tls_sum) ?? 0),
    tls_sum_squares: Math.max(0, readNumber(record.tls_sum_squares) ?? 0),
    latency_histogram: histogram,
  };
}

function valuesFromCollection(value: unknown): unknown[] {
  if (Array.isArray(value)) return value;
  const record = asRecord(value);
  return record ? Object.values(record) : [];
}

function parseSnapshot(value: unknown): {
  targets: Target[];
  samples: Sample[];
  buckets: ChartBucket[];
  stats: Record<string, TargetStats>;
  updatedAt: number;
} {
  const envelope = asRecord(value) ?? {};
  const dataRecord = asRecord(envelope.data);
  const root =
    dataRecord &&
    (dataRecord.targets ||
      dataRecord.samples ||
      dataRecord.stats ||
      dataRecord.monitors)
      ? dataRecord
      : envelope;

  const rawTargets =
    root.targets ?? root.monitors ?? root.items ?? root.endpoints ?? [];
  const targetValues = valuesFromCollection(rawTargets);
  const targets = targetValues
    .map((entry, index) => {
      const record = asRecord(entry);
      return normalizeTarget(record?.target ?? entry, index);
    })
    .filter((target): target is Target => target !== null);

  const samples: Sample[] = [];
  const buckets: ChartBucket[] = [];
  const rawSamples =
    root.samples ?? root.recent_samples ?? root.history ?? root.results;
  if (Array.isArray(rawSamples)) {
    rawSamples.forEach((sample) => {
      const normalized = normalizeSample(sample);
      if (normalized) samples.push(normalized);
    });
  } else {
    const sampleMap = asRecord(rawSamples);
    if (sampleMap) {
      Object.entries(sampleMap).forEach(([targetId, collection]) => {
        valuesFromCollection(collection).forEach((sample) => {
          const normalized = normalizeSample(sample, targetId);
          if (normalized) samples.push(normalized);
        });
      });
    }
  }

  valuesFromCollection(root.chart_samples).forEach((sample) => {
    const normalized = normalizeSample(sample);
    if (normalized) samples.push(normalized);
  });

  valuesFromCollection(root.chart_buckets).forEach((bucket) => {
    const normalized = normalizeChartBucket(bucket);
    if (normalized) buckets.push(normalized);
  });

  const stats: Record<string, TargetStats> = {};
  const rawStats = root.stats ?? root.statistics ?? root.metrics;
  if (Array.isArray(rawStats)) {
    rawStats.forEach((entry) => {
      const record = asRecord(entry);
      if (!record) return;
      const id = readString(record.target_id, record.targetId, record.id);
      if (id) stats[id] = normalizeStats(record);
    });
  } else {
    const statsMap = asRecord(rawStats);
    if (statsMap) {
      Object.entries(statsMap).forEach(([targetId, entry]) => {
        stats[targetId] = normalizeStats(entry);
      });
    }
  }

  targetValues.forEach((entry, index) => {
    const record = asRecord(entry);
    const target = targets[index];
    if (!record || !target) return;
    const nestedStats = record.stats ?? record.statistics ?? record.metrics;
    if (nestedStats) stats[target.id] = normalizeStats(nestedStats);
    const nestedCollections = [
      record.chart_samples,
      record.samples ?? record.recent_samples ?? record.history,
    ];
    nestedCollections.forEach((collection) => {
      valuesFromCollection(collection).forEach((sample) => {
        const normalized = normalizeSample(sample, target.id);
        if (normalized) samples.push(normalized);
      });
    });
    valuesFromCollection(record.chart_buckets).forEach((bucket) => {
      const normalized = normalizeChartBucket(bucket, target.id);
      if (normalized) buckets.push(normalized);
    });
  });

  return {
    targets,
    samples,
    buckets,
    stats,
    updatedAt: toTimestamp(
      root.updated_at ??
        root.updatedAt ??
        root.generated_at ??
        root.ts ??
        root.timestamp ??
        Date.now(),
    ),
  };
}

function mergeSamples(
  previous: Record<string, Sample[]>,
  incoming: Sample[],
): Record<string, Sample[]> {
  if (incoming.length === 0) return previous;
  const next = { ...previous };
  const grouped = new Map<string, Sample[]>();
  incoming.forEach((sample) => {
    const group = grouped.get(sample.target_id) ?? [];
    group.push(sample);
    grouped.set(sample.target_id, group);
  });
  grouped.forEach((samples, targetId) => {
    const combined = [...(next[targetId] ?? []), ...samples].sort(
      (a, b) => a.ts - b.ts,
    );
    const deduplicated: Sample[] = [];
    let lastKey = "";
    combined.forEach((sample) => {
      const key =
        sample.ts +
        "|" +
        sample.status +
        "|" +
        (sample.latency_ms === null ? "" : sample.latency_ms) +
        "|" +
        sample.stage;
      if (key !== lastKey) deduplicated.push(sample);
      lastKey = key;
    });
    next[targetId] = deduplicated.slice(-3000);
  });
  return next;
}

function groupChartBuckets(
  buckets: ChartBucket[],
  targetIds: string[],
): Record<string, ChartBucket[]> {
  const grouped: Record<string, ChartBucket[]> = {};
  const seen = new Set<string>();
  targetIds.forEach((targetId) => {
    grouped[targetId] = [];
  });
  buckets.forEach((bucket) => {
    const key =
      bucket.target_id + "|" + bucket.start_ms + "|" + bucket.duration_ms;
    if (seen.has(key)) return;
    seen.add(key);
    (grouped[bucket.target_id] ??= []).push(bucket);
  });
  Object.values(grouped).forEach((entries) =>
    entries.sort((left, right) => left.start_ms - right.start_ms),
  );
  return grouped;
}

function isEligibleTimeout(sample: Sample): boolean {
  return (
    statusKind(sample.status) === "timeout" &&
    !sample.status.startsWith("local_proxy_")
  );
}

function calculateVolatility(
  count: number,
  sum: number,
  sumSquares: number,
): number | null {
  if (count < 2) return null;
  const mean = sum / count;
  if (mean <= 0) return null;
  const variance = Math.max(0, sumSquares / count - mean * mean);
  return (Math.sqrt(variance) / mean) * 100;
}

function weightedPercentile(
  entries: Array<{ value: number; count: number }>,
  percentile: number,
): number | null {
  const valid = entries
    .filter(
      (entry) =>
        Number.isFinite(entry.value) && entry.value >= 0 && entry.count > 0,
    )
    .sort((left, right) => left.value - right.value);
  const total = valid.reduce((sum, entry) => sum + entry.count, 0);
  if (total === 0) return null;
  const rank = Math.max(1, Math.ceil(total * percentile));
  let cumulative = 0;
  for (const entry of valid) {
    cumulative += entry.count;
    if (cumulative >= rank) return entry.value;
  }
  return valid[valid.length - 1]?.value ?? null;
}

function deriveStats(
  samples: Sample[],
  buckets: ChartBucket[],
  isNode: boolean,
): TargetStats {
  const ordered = samples
    .filter((sample) => sample.bucket_ms === 0)
    .sort((left, right) => left.ts - right.ts);
  const latest = ordered[ordered.length - 1];
  let total = 0;
  let successes = 0;
  let timeouts = 0;
  let refused = 0;
  let tunnelSuccesses = 0;
  let tunnelTimeouts = 0;
  let tlsCount = 0;
  let tlsSum = 0;
  let tlsSumSquares = 0;
  let latencyCount = 0;
  let latencySum = 0;
  let latencySumSquares = 0;
  const latencyDistribution: Array<{ value: number; count: number }> = [];

  ordered.forEach((sample) => {
    total += 1;
    const kind = statusKind(sample.status);
    if (kind === "success") {
      successes += 1;
    } else if (isEligibleTimeout(sample)) {
      timeouts += 1;
    } else if (kind === "refused") {
      refused += 1;
    }
    if (sample.latency_ms !== null && Number.isFinite(sample.latency_ms)) {
      latencyDistribution.push({ value: sample.latency_ms, count: 1 });
      latencyCount += 1;
      latencySum += sample.latency_ms;
      latencySumSquares += sample.latency_ms * sample.latency_ms;
    }
    if (isNode) {
      if (sample.tunnel_ms !== null) tunnelSuccesses += 1;
      else if (sample.stage === "socks" && isEligibleTimeout(sample)) {
        tunnelTimeouts += 1;
      }
      if (sample.tls_ms !== null && Number.isFinite(sample.tls_ms)) {
        tlsCount += 1;
        tlsSum += sample.tls_ms;
        tlsSumSquares += sample.tls_ms * sample.tls_ms;
      }
    }
  });

  buckets.forEach((bucket) => {
    total += bucket.total_count;
    successes += bucket.success_count;
    timeouts += bucket.timeout_count;
    refused += bucket.refused_count;
    latencyCount += bucket.latency_count;
    latencySum += bucket.latency_sum;
    latencySumSquares += bucket.latency_sum_squares;
    if (isNode) {
      tunnelSuccesses += bucket.tunnel_success_count;
      tunnelTimeouts += bucket.tunnel_timeout_count;
      tlsCount += bucket.tls_count;
      tlsSum += bucket.tls_sum;
      tlsSumSquares += bucket.tls_sum_squares;
    }
    bucket.latency_histogram.forEach((bin) => {
      latencyDistribution.push({ value: bin.value_ms, count: bin.count });
    });
  });

  if (total === 0) return { ...EMPTY_STATS };
  // 分钟摘要无需新增丢失计数字段：每个非成功结果（包括保留了
  // 实际延迟的尖峰）都是一次丢失，因此 total - success 始终一致。
  const losses = Math.max(0, total - successes);
  const tunnelDenominator = tunnelSuccesses + tunnelTimeouts;
  return {
    current_ms: latest?.latency_ms ?? null,
    average_ms: latencyCount > 0 ? latencySum / latencyCount : null,
    p95_ms: weightedPercentile(latencyDistribution, 0.95),
    p95_approximate: buckets.some((bucket) => bucket.latency_count > 0),
    local_proxy_current_ms: latest?.local_proxy_ms ?? null,
    tunnel_current_ms: latest?.tunnel_ms ?? null,
    remote_first_byte_current_ms: latest?.remote_first_byte_ms ?? null,
    tls_current_ms: latest?.tls_ms ?? null,
    tls_average_ms: isNode && tlsCount > 0 ? tlsSum / tlsCount : null,
    google_current_ms: latest?.google_ms ?? null,
    success_rate: total > 0 ? (successes / total) * 100 : null,
    timeout_rate: total > 0 ? (timeouts / total) * 100 : null,
    refused_rate: total > 0 ? (refused / total) * 100 : null,
    estimated_loss_rate: (losses / total) * 100,
    tunnel_timeout_rate:
      tunnelDenominator > 0
        ? (tunnelTimeouts / tunnelDenominator) * 100
        : null,
    google_timeout_rate: isNode ? (losses / total) * 100 : null,
    volatility_rate: isNode
      ? calculateVolatility(tlsCount, tlsSum, tlsSumSquares)
      : calculateVolatility(latencyCount, latencySum, latencySumSquares),
  };
}

// 仅供前端统计回归测试使用；生产界面仍只使用默认导出的页面组件。
// eslint-disable-next-line react-refresh/only-export-components
export const __testing = {
  deriveStats,
  buildLatencySeries,
  buildLossEvents,
  lossStatusLabel,
};

function getTargetState(
  target: Target,
  samples: Sample[],
  now: number,
): TargetState {
  if (!target.enabled) {
    return {
      tone: "muted",
      label: "已暂停",
      detail: "此目标暂不执行探测",
      kind: "paused",
    };
  }
  const latest = samples[samples.length - 1];
  if (!latest) {
    return {
      tone: "muted",
      label: "等待数据",
      detail:
        target.kind === "proxy_google"
          ? target.google_204_enabled
            ? "等待首次 Google 204 探测"
            : "等待首次 Google TLS 探测"
          : "等待首次 TCP 探测",
      kind: "waiting",
    };
  }
  if (now - latest.ts > Math.max(target.interval_ms * 3, 10_000)) {
    return {
      tone: "warning",
      label: "等待更新",
      detail: "最后样本 " + formatRelative(latest.ts, now),
      kind: "waiting",
    };
  }
  if (latest.status === "packet_loss") {
    const estimatedSpike = latest.loss_reason === "latency_spike";
    return {
      tone: estimatedSpike ? "warning" : "danger",
      label: lossStatusLabel(latest.status, latest.loss_reason),
      detail: latest.message || lossReasonDetail(latest.loss_reason),
      kind: "error",
    };
  }
  if (latest.status.startsWith("local_proxy_")) {
    return {
      tone: "danger",
      label: "本地代理不可用",
      detail: latest.message || "无法连接本地 SOCKS5 端口",
      kind: "error",
    };
  }
  if (latest.status.startsWith("socks_")) {
    return {
      tone: "danger",
      label: "SOCKS5 异常",
      detail: latest.message || "代理客户端未能建立 Google TCP 隧道",
      kind: "error",
    };
  }
  if (latest.status.startsWith("tls_")) {
    return {
      tone: "danger",
      label: "TLS 异常",
      detail: latest.message || "已建立隧道，但 Google TLS 握手失败",
      kind: "error",
    };
  }
  if (
    latest.status.startsWith("http_") ||
    latest.status === "unexpected_http_status"
  ) {
    return {
      tone: "danger",
      label: "Google 回包异常",
      detail: latest.message || "未收到预期的 HTTP 204",
      kind: "error",
    };
  }
  const kind = statusKind(latest.status);
  if (kind === "success") {
    return {
      tone: "good",
      label: target.kind === "proxy_google" ? "节点可用" : "在线",
      detail:
        target.kind === "proxy_google"
          ? target.google_204_enabled
            ? "已通过节点收到 Google HTTP 204"
            : "已通过节点完成 Google TLS 握手"
          : "TCP 连接正常",
      kind,
    };
  }
  if (kind === "timeout") {
    return {
      tone: "danger",
      label:
        target.kind === "proxy_google"
          ? latest.stage === "socks"
            ? "隧道超时"
            : latest.stage === "tls"
              ? "TLS 超时"
              : "Google 超时"
          : "连接超时",
      detail:
        latest.message ||
        (target.kind === "proxy_google"
          ? "完整节点探测未在超时阈值内完成"
          : "在超时阈值内未建立连接"),
      kind,
    };
  }
  if (kind === "refused") {
    return {
      tone: "warning",
      label: "连接被拒绝",
      detail: latest.message || "主机可达，但端口未接受连接",
      kind,
    };
  }
  if (kind === "dns") {
    return {
      tone: "danger",
      label: "DNS 解析失败",
      detail: latest.message || "无法解析目标主机名",
      kind,
    };
  }
  if (kind === "route") {
    return {
      tone: "danger",
      label: "无可用路由",
      detail: latest.message || "本机到目标网络没有可用路径",
      kind,
    };
  }
  return {
    tone: "danger",
    label: "探测异常",
    detail: latest.message || "TCP 探测未成功",
    kind: "error",
  };
}

function formatMs(value: number | null, digits = 0): string {
  if (value === null || !Number.isFinite(value)) return "—";
  if (value < 10) return value.toFixed(Math.max(digits, 1)) + " ms";
  return Math.round(value) + " ms";
}

function formatPercent(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "—";
  if (value === 0) return "0%";
  if (value < 1) return value.toFixed(1) + "%";
  return Math.round(value) + "%";
}

function formatClock(timestamp: number): string {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(timestamp);
}

function formatRelative(timestamp: number, now: number): string {
  const seconds = Math.max(0, Math.round((now - timestamp) / 1000));
  if (seconds < 5) return "刚刚";
  if (seconds < 60) return seconds + " 秒前";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return minutes + " 分钟前";
  return Math.floor(minutes / 60) + " 小时前";
}

function colorForIndex(index: number): string {
  return SERIES_COLORS[index % SERIES_COLORS.length];
}

function niceCeiling(value: number): number {
  if (value <= 0) return 50;
  const power = Math.pow(10, Math.floor(Math.log10(value)));
  const normalized = value / power;
  const step =
    normalized <= 1
      ? 1
      : normalized <= 2
        ? 2
        : normalized <= 5
          ? 5
          : 10;
  return step * power;
}

function apiUrl(path: string): string {
  return API_BASE + path;
}

async function apiRequest(
  path: string,
  init?: RequestInit,
): Promise<unknown> {
  let response: Response;
  try {
    response = await fetch(apiUrl(path), {
      ...init,
      mode: "cors",
      credentials: "omit",
      cache: "no-store",
      referrerPolicy: "no-referrer",
      headers: {
        Accept: "application/json",
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...init?.headers,
      },
    });
  } catch (error) {
    if (error instanceof TypeError) {
      throw new Error(SERVICE_UNAVAILABLE_MESSAGE, { cause: error });
    }
    throw error;
  }
  if (!response.ok) {
    let detail = "";
    try {
      const payload = (await response.json()) as unknown;
      const record = asRecord(payload);
      const nestedError = asRecord(record?.error);
      detail = readString(
        nestedError?.message,
        record?.message,
        record?.error,
        record?.detail,
      );
    } catch {
      detail = await response.text().catch(() => "");
    }
    throw new Error(detail || "请求失败（HTTP " + response.status + "）");
  }
  if (response.status === 204) return null;
  const contentType = response.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) {
    throw new Error(
      "端口 9288 返回的不是链路哨兵接口响应，可能已被其他程序占用。",
    );
  }
  return response.json();
}

function buildLatencySeries(
  targets: Target[],
  samplesByTarget: Record<string, Sample[]>,
  visible: Record<string, boolean>,
): ChartSeries[] {
  return targets.flatMap((target, index) => {
    if (visible[target.id] === false) return [];
    const samples = samplesByTarget[target.id] ?? [];
    if (target.kind === "proxy_google") {
      return [
        {
          id: target.id + ":tls-complete",
          name: target.name + " · TLS完成",
          color: colorForIndex(index),
          intervalMs: target.interval_ms,
          dash: [],
          points: samples.map((sample) => ({
            ts: sample.ts,
            value:
              sample.bucket_ms > 0 && statusKind(sample.status) !== "success"
                ? null
                : sample.tls_ms,
            bucketMs: sample.bucket_ms,
          })),
        },
      ];
    }
    return [
      {
        id: target.id,
        name: target.name,
        color: colorForIndex(index),
        intervalMs: target.interval_ms,
        points: samples.map((sample) => ({
          ts: sample.ts,
          bucketMs: sample.bucket_ms,
          value:
            sample.bucket_ms > 0 && statusKind(sample.status) !== "success"
              ? null
              : sample.latency_ms,
        })),
      },
    ];
  });
}

function probeStageLabel(stage: string): string {
  switch (stage) {
    case "local_proxy":
      return "本地代理";
    case "socks":
      return "SOCKS";
    case "tls":
      return "TLS";
    case "http":
      return "HTTP";
    case "tcp":
      return "TCP";
    default:
      return "连接";
  }
}

function lossStatusLabel(status: string, lossReason: string): string {
  switch (lossReason) {
    case "latency_spike":
      return "延迟尖峰";
  }

  if (status === "packet_loss") return "丢失";
  if (status.includes("certificate")) return "证书错误";
  if (status.includes("auth")) return "认证失败";
  if (status.includes("protocol")) return "协议错误";
  if (status.includes("reset")) return "连接被重置";
  const kind = statusKind(status);
  switch (kind) {
    case "timeout":
      return "超时";
    case "refused":
      return "连接被拒绝";
    case "dns":
      return "DNS 解析失败";
    case "route":
      return "网络不可达";
    default:
      if (status.startsWith("socks_")) return "SOCKS 异常";
      if (status.startsWith("tls_")) return "TLS 异常";
      if (status.startsWith("http_") || status === "unexpected_http_status") {
        return "HTTP 异常";
      }
      if (status.startsWith("local_proxy_")) return "本地代理异常";
      return "探测失败";
  }
}

function lossReasonDetail(lossReason: string): string {
  switch (lossReason) {
    case "latency_spike":
      return "本次延迟超过目标滚动基准的动态阈值，已按一次推定丢包计入";
    default:
      return "本次逻辑探测已计入丢包率";
  }
}

function buildLossEvents(
  targets: Target[],
  samplesByTarget: Record<string, Sample[]>,
  visible: Record<string, boolean>,
): LossEvent[] {
  return targets.flatMap((target, targetIndex) => {
    if (visible[target.id] === false) return [];
    return (samplesByTarget[target.id] ?? []).flatMap(
      (sample, sampleIndex): LossEvent[] => {
        if (statusKind(sample.status) === "success") return [];
        const estimated = sample.loss_reason === "latency_spike";
        const measuredValue = estimated
          ? target.kind === "proxy_google"
            ? sample.tls_ms
            : sample.latency_ms
          : null;
        return [
          {
            id:
              target.id + ":loss:" + sample.ts + ":" + sampleIndex,
            name: target.name,
            color: colorForIndex(targetIndex),
            markerColor: estimated ? "#f7b84b" : "#ff748c",
            kind: estimated ? "estimated" : "failure",
            value:
              measuredValue !== null && Number.isFinite(measuredValue)
                ? measuredValue
                : null,
            ts: sample.ts,
            stage: probeStageLabel(sample.stage),
            status: sample.status,
            lossReason: sample.loss_reason,
            bucketMs: sample.bucket_ms,
          },
        ];
      },
    );
  });
}

function TargetModal({
  target,
  saving,
  error,
  onClose,
  onSubmit,
}: {
  target: Target | null;
  saving: boolean;
  error: string;
  onClose: () => void;
  onSubmit: (values: TargetFormValues) => Promise<void>;
}) {
  const [values, setValues] = useState<TargetFormValues>(() =>
    target
      ? {
          name: target.name,
          kind: target.kind,
          host: target.host,
          port: String(target.port),
          proxy_host: target.proxy_host || "127.0.0.1",
          proxy_port: String(target.proxy_port || 10808),
          google_204_enabled: target.google_204_enabled,
          interval_ms: String(target.interval_ms),
          timeout_ms: String(target.timeout_ms),
          enabled: target.enabled,
        }
      : DEFAULT_FORM,
  );
  const [validation, setValidation] = useState("");

  const update = (key: keyof TargetFormValues, value: string | boolean) => {
    setValues((previous) => ({ ...previous, [key]: value }));
    setValidation("");
  };

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const port = Number(values.port);
    const proxyPort = Number(values.proxy_port);
    const interval = Number(values.interval_ms);
    const timeout = Number(values.timeout_ms);
    if (!values.name.trim()) {
      setValidation("请填写便于识别的目标名称。");
      return;
    }
    if (values.kind === "direct_tcp" && !values.host.trim()) {
      setValidation("请填写主机名或 IP 地址。");
      return;
    }
    if (
      values.kind === "direct_tcp" &&
      (!Number.isInteger(port) || port < 1 || port > 65535)
    ) {
      setValidation("端口必须是 1–65535 之间的整数。");
      return;
    }
    if (
      values.kind === "proxy_google" &&
      !["127.0.0.1", "localhost", "::1", "[::1]"].includes(
        values.proxy_host.trim().toLowerCase(),
      )
    ) {
      setValidation("节点探测的 SOCKS5 地址必须是本机回环地址。");
      return;
    }
    if (
      values.kind === "proxy_google" &&
      (!Number.isInteger(proxyPort) || proxyPort < 1 || proxyPort > 65535)
    ) {
      setValidation("SOCKS5 端口必须是 1–65535 之间的整数。");
      return;
    }
    const minimumInterval = values.kind === "proxy_google" ? 2000 : 500;
    if (
      !Number.isFinite(interval) ||
      interval < minimumInterval ||
      interval > 3_600_000
    ) {
      setValidation(
        values.kind === "proxy_google"
          ? "Google 节点探测间隔需在 2 秒至 1 小时之间。"
          : "探测间隔需在 500 毫秒至 1 小时之间。",
      );
      return;
    }
    if (!Number.isFinite(timeout) || timeout < 100 || timeout > 60_000) {
      setValidation("连接超时需在 100 毫秒至 1 分钟之间。");
      return;
    }
    await onSubmit({
      ...values,
      name: values.name.trim(),
      host:
        values.kind === "proxy_google"
          ? GOOGLE_PROBE_HOST
          : values.host.trim(),
      port:
        values.kind === "proxy_google"
          ? String(GOOGLE_PROBE_PORT)
          : values.port,
      proxy_host:
        values.kind === "proxy_google"
          ? values.proxy_host.trim()
          : "",
      proxy_port:
        values.kind === "proxy_google" ? values.proxy_port : "0",
    });
  };

  return (
    <div
      className="modal-backdrop drawer-backdrop"
      role="presentation"
      onPointerDown={(event) => {
        if (event.currentTarget === event.target && !saving) onClose();
      }}
    >
      <section
        className="modal-card target-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="target-modal-title"
      >
        <div className="modal-head">
          <div>
            <p className="eyebrow drawer-context">
              {values.kind === "proxy_google" ? "节点探测" : "TCP 目标"}
            </p>
            <h2 id="target-modal-title">
              {target ? "编辑监控目标" : "添加监控目标"}
            </h2>
          </div>
          <button
            className="icon-button"
            type="button"
            aria-label="关闭"
            onClick={onClose}
            disabled={saving}
          >
            ×
          </button>
        </div>

        <form onSubmit={submit}>
          <div className="probe-kind-picker" role="group" aria-label="探测类型">
            <button
              type="button"
              className={values.kind === "direct_tcp" ? "active" : ""}
              aria-pressed={values.kind === "direct_tcp"}
              onClick={() =>
                setValues((previous) => ({
                  ...previous,
                  kind: "direct_tcp",
                  interval_ms:
                    previous.kind === "proxy_google" ? "2000" : previous.interval_ms,
                  timeout_ms:
                    previous.kind === "proxy_google" ? "1500" : previous.timeout_ms,
                }))
              }
            >
              <strong>直接 TCP</strong>
              <small>监控指定主机和端口</small>
            </button>
            <button
              type="button"
              className={values.kind === "proxy_google" ? "active" : ""}
              aria-pressed={values.kind === "proxy_google"}
              onClick={() =>
                setValues((previous) => ({
                  ...previous,
                  kind: "proxy_google",
                  interval_ms:
                    previous.kind === "direct_tcp" ? "5000" : previous.interval_ms,
                  timeout_ms:
                    previous.kind === "direct_tcp" ? "8000" : previous.timeout_ms,
                }))
              }
            >
              <strong>节点探测</strong>
              <small>SOCKS5 → 代理节点 → Google TLS</small>
            </button>
          </div>
          <div className="form-grid">
            <label className="field field-wide">
              <span>目标名称</span>
              <input
                autoFocus
                value={values.name}
                onChange={(event) => update("name", event.target.value)}
                placeholder={
                  values.kind === "proxy_google"
                    ? "例如：当前香港节点"
                    : "例如：香港 API 网关"
                }
                maxLength={80}
              />
            </label>
            {values.kind === "direct_tcp" ? (
              <>
                <label className="field">
                  <span>主机名或 IP</span>
                  <input
                    value={values.host}
                    onChange={(event) => update("host", event.target.value)}
                    placeholder="api.example.com"
                    spellCheck={false}
                    maxLength={253}
                  />
                </label>
                <label className="field">
                  <span>TCP 端口</span>
                  <input
                    inputMode="numeric"
                    type="number"
                    min="1"
                    max="65535"
                    value={values.port}
                    onChange={(event) => update("port", event.target.value)}
                  />
                </label>
              </>
            ) : (
              <>
                <div className="node-endpoint-note field-wide">
                  <span>固定测试终点</span>
                  <strong>
                    {values.google_204_enabled
                      ? `https://${GOOGLE_PROBE_HOST}${GOOGLE_PROBE_PATH}`
                      : `${GOOGLE_PROBE_HOST}:${GOOGLE_PROBE_PORT}`}
                  </strong>
                  <small>
                    {values.google_204_enabled
                      ? "TLS 完成后继续发送请求并验证 HTTP 204"
                      : "默认在 TLS 握手完成后结束，不发送 HTTP 请求"}
                  </small>
                </div>
                <label className="enable-row node-http-option">
                  <span>
                    <strong>继续验证 Google HTTP 204</strong>
                    <small>增加一次 HTTP 请求响应；默认关闭</small>
                  </span>
                  <input
                    className="switch-input"
                    type="checkbox"
                    checked={values.google_204_enabled}
                    onChange={(event) =>
                      update("google_204_enabled", event.target.checked)
                    }
                  />
                  <span className="switch-track" aria-hidden="true" />
                </label>
                <label className="field">
                  <span>本地 SOCKS5 地址</span>
                  <input
                    value={values.proxy_host}
                    onChange={(event) => update("proxy_host", event.target.value)}
                    placeholder="127.0.0.1"
                    spellCheck={false}
                  />
                </label>
                <label className="field">
                  <span>SOCKS5 端口</span>
                  <input
                    inputMode="numeric"
                    type="number"
                    min="1"
                    max="65535"
                    value={values.proxy_port}
                    onChange={(event) => update("proxy_port", event.target.value)}
                  />
                </label>
              </>
            )}
            <label className="field">
              <span>探测间隔（毫秒）</span>
              <input
                inputMode="numeric"
                type="number"
                min={values.kind === "proxy_google" ? "2000" : "500"}
                max="3600000"
                step="100"
                value={values.interval_ms}
                onChange={(event) => update("interval_ms", event.target.value)}
              />
            </label>
            <label className="field">
              <span>连接超时（毫秒）</span>
              <input
                inputMode="numeric"
                type="number"
                min="100"
                max="60000"
                step="100"
                value={values.timeout_ms}
                onChange={(event) => update("timeout_ms", event.target.value)}
              />
            </label>
          </div>

          <label className="enable-row">
            <span>
              <strong>立即开始监控</strong>
              <small>
                {values.kind === "proxy_google"
                  ? values.google_204_enabled
                    ? "保存后经本地 SOCKS5 持续验证 Google TLS 与 HTTP 204"
                    : "保存后经本地 SOCKS5 持续验证 Google TLS"
                  : "保存后按设定间隔发起 TCP 连接探测"}
              </small>
            </span>
            <input
              className="switch-input"
              type="checkbox"
              checked={values.enabled}
              onChange={(event) => update("enabled", event.target.checked)}
            />
            <span className="switch-track" aria-hidden="true" />
          </label>

          {(validation || error) && (
            <p className="form-error" role="alert">
              {validation || error}
            </p>
          )}

          <div className="modal-actions">
            <button
              className="button button-secondary"
              type="button"
              onClick={onClose}
              disabled={saving}
            >
              取消
            </button>
            <button className="button button-primary" type="submit" disabled={saving}>
              {saving ? "正在保存…" : target ? "保存更改" : "添加目标"}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}

function DeleteModal({
  target,
  deleting,
  error,
  onClose,
  onConfirm,
}: {
  target: Target;
  deleting: boolean;
  error: string;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <div
      className="modal-backdrop"
      role="presentation"
      onPointerDown={(event) => {
        if (event.currentTarget === event.target && !deleting) onClose();
      }}
    >
      <section
        className="modal-card modal-card-small"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="delete-modal-title"
        aria-describedby="delete-modal-description"
      >
        <div className="danger-mark" aria-hidden="true">
          !
        </div>
        <h2 id="delete-modal-title">删除“{target.name}”？</h2>
        <p id="delete-modal-description" className="modal-copy">
          该目标的监控配置将被删除。此操作不会影响远端服务器。
        </p>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="modal-actions">
          <button
            className="button button-secondary"
            type="button"
            onClick={onClose}
            disabled={deleting}
          >
            取消
          </button>
          <button
            className="button button-danger"
            type="button"
            onClick={onConfirm}
            disabled={deleting}
            autoFocus
          >
            {deleting ? "正在删除…" : "确认删除"}
          </button>
        </div>
      </section>
    </div>
  );
}

function isChartDatumInWindow(
  timestamp: number,
  bucketMS: number,
  start: number,
  end: number,
): boolean {
  if (bucketMS <= 0) return timestamp >= start && timestamp <= end;
  const bucketStart = timestamp - (timestamp % bucketMS);
  return bucketStart >= start && bucketStart + bucketMS <= end;
}

function CanvasChart({
  series,
  events,
  windowMs,
  anchorTime,
}: {
  series: ChartSeries[];
  events: LossEvent[];
  windowMs: number;
  anchorTime: number;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const yMaxRef = useRef(100);
  const [tooltip, setTooltip] = useState<TooltipState | null>(null);

  const visiblePoints = useMemo(
    () =>
      series.flatMap((item) =>
        item.points.filter(
          (point) =>
            isChartDatumInWindow(
              point.ts,
              point.bucketMs,
              anchorTime - windowMs,
              anchorTime,
            ) &&
            point.value !== null,
        ),
      ),
    [series, anchorTime, windowMs],
  );

  const visibleEvents = useMemo(
    () =>
      events.filter(
        (event) =>
          isChartDatumInWindow(
            event.ts,
            event.bucketMs,
            anchorTime - windowMs,
            anchorTime,
          ),
      ),
    [events, anchorTime, windowMs],
  );

  const draw = useCallback(() => {
    const wrap = wrapRef.current;
    const canvas = canvasRef.current;
    if (!wrap || !canvas) return;
    const rect = wrap.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.round(rect.width);
    const height = Math.round(rect.height);
    if (
      canvas.width !== Math.round(width * dpr) ||
      canvas.height !== Math.round(height * dpr)
    ) {
      canvas.width = Math.round(width * dpr);
      canvas.height = Math.round(height * dpr);
      canvas.style.width = width + "px";
      canvas.style.height = height + "px";
    }
    const context = canvas.getContext("2d");
    if (!context) return;
    context.setTransform(dpr, 0, 0, dpr, 0, 0);
    context.clearRect(0, 0, width, height);

    const plot = {
      x: width < 520 ? 52 : 62,
      y: 18,
      width: Math.max(10, width - (width < 520 ? 68 : 84)),
      height: Math.max(10, height - 54),
    };
    const start = anchorTime - windowMs;

    const observedMax = Math.max(
      0,
      ...visiblePoints.map((point) => point.value ?? 0),
      ...visibleEvents.map((event) => event.value ?? 0),
    );
    const desired = niceCeiling(Math.max(40, observedMax * 1.16));
    if (
      desired > yMaxRef.current ||
      desired < yMaxRef.current * 0.48
    ) {
      yMaxRef.current = desired;
    }
    const yMax = yMaxRef.current;
    const xFor = (timestamp: number) =>
      plot.x + ((timestamp - start) / windowMs) * plot.width;
    const yFor = (value: number) =>
      plot.y + plot.height - (value / yMax) * plot.height;

    context.font =
      (width < 520 ? "11px" : "12px") +
      ' "Cascadia Mono", "SFMono-Regular", Consolas, monospace';
    context.textBaseline = "middle";
    for (let index = 0; index <= 4; index += 1) {
      const value = (yMax / 4) * index;
      const y = yFor(value);
      context.beginPath();
      context.setLineDash(index === 0 ? [] : [3, 5]);
      context.strokeStyle =
        index === 0 ? "rgba(129, 151, 181, .24)" : "rgba(129, 151, 181, .12)";
      context.lineWidth = 1;
      context.moveTo(plot.x, y + 0.5);
      context.lineTo(plot.x + plot.width, y + 0.5);
      context.stroke();
      context.setLineDash([]);
      context.fillStyle = "#9cabbf";
      context.textAlign = "right";
      context.fillText(Math.round(value) + " ms", plot.x - 8, y);
    }

    const timeFormatter = new Intl.DateTimeFormat("zh-CN", {
      hour: "2-digit",
      minute: "2-digit",
      second: windowMs <= 60_000 ? "2-digit" : undefined,
      hour12: false,
    });
    for (let index = 0; index <= 4; index += 1) {
      const timestamp = start + (windowMs / 4) * index;
      const x = xFor(timestamp);
      context.fillStyle = "#9cabbf";
      context.textAlign =
        index === 0 ? "left" : index === 4 ? "right" : "center";
      context.fillText(
        timeFormatter.format(timestamp),
        x,
        plot.y + plot.height + 24,
      );
    }

    context.save();
    context.beginPath();
    context.rect(plot.x, plot.y, plot.width, plot.height);
    context.clip();

    series.forEach((item, seriesIndex) => {
      const points = item.points.filter(
        (point) =>
          point.bucketMs > 0
            ? isChartDatumInWindow(
                point.ts,
                point.bucketMs,
                start,
                anchorTime,
              )
            : point.ts >= start - item.intervalMs * 2 &&
              point.ts <= anchorTime,
      );
      context.beginPath();
      context.strokeStyle = item.color;
      context.lineWidth = 2;
      context.lineJoin = "round";
      context.lineCap = "round";
      context.globalAlpha = 0.92;
      const dash = item.dash ??
        (seriesIndex % 4 === 1
          ? [8, 5]
          : seriesIndex % 4 === 2
            ? [2, 4]
            : seriesIndex % 4 === 3
              ? [10, 4, 2, 4]
              : []);
      context.setLineDash(dash);
      let drawing = false;
      let previousTs = 0;
      let previousBucketMs = 0;
      points.forEach((point) => {
        const allowedGap = Math.max(
          item.intervalMs * 1.8,
          point.bucketMs * 1.8,
          previousBucketMs * 1.8,
          2500,
        );
        if (
          point.value === null ||
          (previousTs > 0 && point.ts - previousTs > allowedGap)
        ) {
          drawing = false;
          previousTs = point.ts;
          previousBucketMs = point.bucketMs;
          return;
        }
        const x = xFor(point.ts);
        const y = yFor(point.value);
        if (!drawing) {
          context.moveTo(x, y);
          drawing = true;
        } else {
          context.lineTo(x, y);
        }
        previousTs = point.ts;
        previousBucketMs = point.bucketMs;
      });
      context.stroke();
      context.setLineDash([]);
      context.globalAlpha = 1;

      const latest = [...points]
        .reverse()
        .find((point) => point.value !== null);
      if (latest && latest.value !== null) {
        const x = xFor(latest.ts);
        const y = yFor(latest.value);
        context.beginPath();
        context.fillStyle = "#101722";
        context.strokeStyle = item.color;
        context.lineWidth = 2;
        context.arc(x, y, 3.2, 0, Math.PI * 2);
        context.fill();
        context.stroke();
      }
    });
    context.restore();

    const eventStacks = new Map<number, number>();
    visibleEvents.forEach((event) => {
      const x = xFor(event.ts);
      if (event.kind === "estimated" && event.value !== null) {
        const y = yFor(event.value);
        context.beginPath();
        context.fillStyle = "#101722";
        context.strokeStyle = event.markerColor;
        context.lineWidth = 2.5;
        context.arc(x, y, 5, 0, Math.PI * 2);
        context.fill();
        context.stroke();
        return;
      }
      const key = Math.round(x);
      const stack = eventStacks.get(key) ?? 0;
      eventStacks.set(key, stack + 1);
      const y = plot.y + plot.height - 6 - stack * 11;
      context.beginPath();
      context.moveTo(x, y - 9);
      context.lineTo(x - 5.5, y);
      context.lineTo(x + 5.5, y);
      context.closePath();
      context.fillStyle = event.markerColor;
      context.strokeStyle = event.color;
      context.lineWidth = 1.5;
      context.fill();
      context.stroke();
    });

    if (visiblePoints.length === 0 && visibleEvents.length === 0) {
      context.fillStyle = "#8a98aa";
      context.font = "600 14px system-ui, sans-serif";
      context.textAlign = "center";
      context.fillText(
        "等待采样数据…",
        plot.x + plot.width / 2,
        plot.y + plot.height / 2,
      );
    }
  }, [anchorTime, series, visibleEvents, visiblePoints, windowMs]);

  useEffect(() => {
    draw();
    const wrap = wrapRef.current;
    if (!wrap) return;
    const observer = new ResizeObserver(draw);
    observer.observe(wrap);
    return () => observer.disconnect();
  }, [draw]);

  const updateTooltip = useCallback(
    (timestamp: number, x: number, width: number) => {
      const latencyRows = series.map((item) => {
        let nearest: ChartPoint | null = null;
        let distance = Number.POSITIVE_INFINITY;
        for (let index = item.points.length - 1; index >= 0; index -= 1) {
          const point = item.points[index];
          if (
            !isChartDatumInWindow(
              point.ts,
              point.bucketMs,
              anchorTime - windowMs,
              anchorTime,
            )
          ) {
            continue;
          }
          const delta = Math.abs(point.ts - timestamp);
          if (delta < distance) {
            nearest = point;
            distance = delta;
          }
          if (point.ts < timestamp && delta > distance) break;
        }
        const valid =
          nearest &&
          distance <=
            Math.max(
              item.intervalMs * 2,
              nearest.bucketMs * 1.2,
              Math.min(windowMs / 80, 120_000),
            )
            ? nearest.value
            : null;
        return {
          id: item.id,
          name:
            item.name +
            (nearest && nearest.bucketMs > 0 ? " · 分钟均值" : ""),
          color: item.color,
          value: valid,
        };
      });
      const lossRows = events
        .filter(
          (event) =>
            isChartDatumInWindow(
              event.ts,
              event.bucketMs,
              anchorTime - windowMs,
              anchorTime,
            ) &&
            Math.abs(event.ts - timestamp) <=
            Math.max(
              1000,
              event.bucketMs / 2,
              Math.min(windowMs / 80, 120_000),
            ),
        )
        .sort(
          (left, right) =>
            Math.abs(left.ts - timestamp) - Math.abs(right.ts - timestamp),
        )
        .map((event) => ({
          id: event.id,
          name: event.name,
          color: event.markerColor,
          value: null,
          tone: event.kind,
          text:
            event.stage +
            " · " +
            lossStatusLabel(event.status, event.lossReason) +
            (event.value !== null ? " · " + formatMs(event.value, 1) : "") +
            (event.bucketMs > 0 ? "（分钟摘要）" : ""),
        }));
      setTooltip({
        x,
        alignRight: x > width * 0.68,
        ts: timestamp,
        rows: [...lossRows, ...latencyRows],
      });
    },
    [anchorTime, events, series, windowMs],
  );

  const handlePointerMove = (event: PointerEvent<HTMLCanvasElement>) => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    const rect = wrap.getBoundingClientRect();
    const left = rect.width < 520 ? 52 : 62;
    const right = rect.width - (rect.width < 520 ? 16 : 22);
    const x = Math.max(left, Math.min(right, event.clientX - rect.left));
    const timestamp =
      anchorTime -
      windowMs +
      ((x - left) / Math.max(1, right - left)) * windowMs;
    updateTooltip(timestamp, x, rect.width);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLCanvasElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const wrap = wrapRef.current;
    if (!wrap) return;
    const rect = wrap.getBoundingClientRect();
    const left = rect.width < 520 ? 52 : 62;
    const right = rect.width - (rect.width < 520 ? 16 : 22);
    let timestamp = tooltip?.ts ?? anchorTime;
    if (event.key === "Home") timestamp = anchorTime - windowMs;
    if (event.key === "End") timestamp = anchorTime;
    if (event.key === "ArrowLeft") timestamp -= windowMs / 40;
    if (event.key === "ArrowRight") timestamp += windowMs / 40;
    timestamp = Math.max(anchorTime - windowMs, Math.min(anchorTime, timestamp));
    const x =
      left +
      ((timestamp - (anchorTime - windowMs)) / windowMs) * (right - left);
    updateTooltip(timestamp, x, rect.width);
  };

  const chartLabel = "TCP 建连、节点 TLS 完成延迟及丢失事件实时图";

  return (
    <div className="chart-wrap chart-latency" ref={wrapRef}>
      <canvas
        ref={canvasRef}
        className="chart-canvas"
        role="img"
        aria-label={chartLabel}
        tabIndex={0}
        onPointerMove={handlePointerMove}
        onPointerLeave={() => setTooltip(null)}
        onFocus={(event) => {
          const rect = event.currentTarget.getBoundingClientRect();
          updateTooltip(anchorTime, rect.width - 22, rect.width);
        }}
        onBlur={() => setTooltip(null)}
        onKeyDown={handleKeyDown}
      >
        {chartLabel}
      </canvas>
      {tooltip && (
        <>
          <span
            className="chart-crosshair"
            aria-hidden="true"
            style={{ left: tooltip.x }}
          />
          <div
            className={
              "chart-tooltip" + (tooltip.alignRight ? " tooltip-right" : "")
            }
            style={{ left: tooltip.x }}
          >
            <strong>{formatClock(tooltip.ts)}</strong>
            {tooltip.rows.slice(0, 8).map((row) => (
              <span key={row.id}>
                <i style={{ backgroundColor: row.color }} />
                <em>{row.name}</em>
                <b
                  className={
                    row.text
                      ? "loss-value" +
                        (row.tone === "estimated" ? " estimated" : "")
                      : undefined
                  }
                >
                  {row.text ?? formatMs(row.value, 1)}
                </b>
              </span>
            ))}
          </div>
        </>
      )}
      <p className="sr-only">
        {visiblePoints.length === 0 && visibleEvents.length === 0
          ? "当前时间范围内暂无采样数据。"
          : "当前时间范围内有 " +
            visiblePoints.length +
            " 个有效延迟点，" +
            visibleEvents.length +
            " 个丢失事件。"}
      </p>
    </div>
  );
}

function targetEndpointText(target: Target): string {
  return target.kind === "proxy_google"
    ? `SOCKS ${target.proxy_host}:${target.proxy_port} → Google TLS${
        target.google_204_enabled ? " + 204" : ""
      }`
    : `${target.host}:${target.port}`;
}

function MetricTile({
  label,
  value,
  title,
  lossRangeLabel,
}: {
  label: string;
  value: string;
  title?: string;
  lossRangeLabel?: string;
}) {
  return (
    <div className="metric-tile">
      <dt
        className={lossRangeLabel ? "metric-with-help" : undefined}
        title={title}
      >
        {lossRangeLabel ? (
          <LossRateLabel rangeLabel={lossRangeLabel} />
        ) : (
          label
        )}
      </dt>
      <dd>{value}</dd>
    </div>
  );
}

function TargetActionMenu({
  target,
  onEdit,
  onToggle,
  onDelete,
}: {
  target: Target;
  onEdit: (target: Target) => void;
  onToggle: (target: Target) => void;
  onDelete: (target: Target) => void;
}) {
  const closeMenu = (element: HTMLElement) => {
    const details = element.closest("details");
    if (details instanceof HTMLDetailsElement) details.open = false;
  };

  return (
    <details
      className="target-action-menu"
      onBlur={(event) => {
        const nextFocus = event.relatedTarget;
        if (
          !(nextFocus instanceof Node) ||
          !event.currentTarget.contains(nextFocus)
        ) {
          event.currentTarget.open = false;
        }
      }}
      onKeyDown={(event) => {
        if (event.key !== "Escape") return;
        event.currentTarget.open = false;
        event.currentTarget.querySelector("summary")?.focus();
      }}
    >
      <summary aria-label={`打开 ${target.name} 的操作菜单`}>•••</summary>
      <div className="target-action-popover" role="menu">
        <button
          type="button"
          role="menuitem"
          onClick={(event) => {
            closeMenu(event.currentTarget);
            onEdit(target);
          }}
        >
          编辑目标
        </button>
        <button
          type="button"
          role="menuitem"
          onClick={(event) => {
            closeMenu(event.currentTarget);
            onToggle(target);
          }}
        >
          {target.enabled ? "暂停监测" : "恢复监测"}
        </button>
        <button
          className="danger-text"
          type="button"
          role="menuitem"
          onClick={(event) => {
            closeMenu(event.currentTarget);
            onDelete(target);
          }}
        >
          删除目标
        </button>
      </div>
    </details>
  );
}

function DesktopSidebar({
  targets,
  statsByTarget,
  statesByTarget,
  selectedTargetId,
  visibleTargets,
  connection,
  connectionCopy,
  lastUpdated,
  now,
  onSelect,
  onToggleChart,
}: {
  targets: Target[];
  statsByTarget: Record<string, TargetStats>;
  statesByTarget: Record<string, TargetState>;
  selectedTargetId: string | null;
  visibleTargets: Record<string, boolean>;
  connection: ConnectionState;
  connectionCopy: string;
  lastUpdated: number | null;
  now: number;
  onSelect: (targetId: string | null) => void;
  onToggleChart: (targetId: string) => void;
}) {
  const groups = [
    {
      key: "proxy_google",
      label: "节点探测",
      targets: targets.filter((target) => target.kind === "proxy_google"),
    },
    {
      key: "direct_tcp",
      label: "TCP 监测",
      targets: targets.filter((target) => target.kind === "direct_tcp"),
    },
  ];
  const onlineCount = targets.filter(
    (target) => statesByTarget[target.id]?.kind === "success",
  ).length;

  return (
    <aside className="desktop-sidebar" aria-label="监控目标导航">
      <div className="sidebar-brand" aria-label="链路哨兵">
        <span className="brand-mark" aria-hidden="true">
          <i />
          <i />
          <i />
        </span>
        <span>
          <strong>链路哨兵</strong>
          <small>NetWatch</small>
        </span>
      </div>

      <div className="sidebar-section-title">
        <span>监测工作区</span>
      </div>
      <button
        className={
          "sidebar-overview" + (selectedTargetId === null ? " active" : "")
        }
        type="button"
        aria-pressed={selectedTargetId === null}
        onClick={() => onSelect(null)}
      >
        <span className="overview-icon" aria-hidden="true">
          <i />
          <i />
          <i />
          <i />
        </span>
        <span>
          <strong>全部目标</strong>
          <small>
            {onlineCount} 在线 / {targets.length} 总计
          </small>
        </span>
      </button>

      <nav className="sidebar-targets" aria-label="目标列表">
        {groups.map((group) =>
          group.targets.length > 0 ? (
            <div className="sidebar-target-group" key={group.key}>
              <div className="sidebar-group-label">
                <span>{group.label}</span>
                <b>{group.targets.length}</b>
              </div>
              {group.targets.map((target) => {
                const index = targets.findIndex((item) => item.id === target.id);
                const state =
                  statesByTarget[target.id] ??
                  ({ tone: "muted", label: "等待数据", kind: "waiting" } as TargetState);
                const stats = statsByTarget[target.id] ?? EMPTY_STATS;
                const chartVisible = visibleTargets[target.id] !== false;
                return (
                  <div
                    className={
                      "sidebar-target-row" +
                      (selectedTargetId === target.id ? " active" : "")
                    }
                    key={target.id}
                  >
                    <button
                      className="sidebar-target-select"
                      type="button"
                      aria-pressed={selectedTargetId === target.id}
                      onClick={() => onSelect(target.id)}
                    >
                      <i
                        className={`sidebar-target-dot tone-${state.tone}`}
                        style={{ borderColor: colorForIndex(Math.max(0, index)) }}
                        aria-hidden="true"
                      />
                      <span>
                        <strong>{target.name}</strong>
                        <small>{targetEndpointText(target)}</small>
                      </span>
                      <b>
                        {state.kind === "success"
                          ? formatMs(stats.current_ms, 1)
                          : state.label}
                      </b>
                    </button>
                    <button
                      className="chart-visibility-toggle"
                      type="button"
                      aria-label={`${chartVisible ? "从综合图表隐藏" : "在综合图表显示"}${target.name}`}
                      aria-pressed={chartVisible}
                      onClick={() => onToggleChart(target.id)}
                    >
                      <span aria-hidden="true" />
                    </button>
                  </div>
                );
              })}
            </div>
          ) : null,
        )}
      </nav>

      <div className="sidebar-service" aria-live="polite">
        <span className={`service-indicator connection-${connection}`}>
          <i aria-hidden="true" />
          {connectionCopy}
        </span>
        <small>
          最后更新 {lastUpdated ? formatRelative(lastUpdated, now) : "等待数据"}
        </small>
      </div>
    </aside>
  );
}

export default function Home() {
  const [targets, setTargets] = useState<Target[]>([]);
  const [samplesByTarget, setSamplesByTarget] = useState<
    Record<string, Sample[]>
  >({});
  const [chartBucketsByTarget, setChartBucketsByTarget] = useState<
    Record<string, ChartBucket[]>
  >({});
  const [connection, setConnection] =
    useState<ConnectionState>("connecting");
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [rangeMs, setRangeMs] = useState(
    () => loadViewPreferences().rangeMs,
  );
  const [visibleTargets, setVisibleTargets] = useState<Record<string, boolean>>(
    () => loadViewPreferences().visibleTargets,
  );
  const [selectedTargetId, setSelectedTargetId] = useState<string | null>(
    () => loadViewPreferences().selectedTargetId,
  );
  const [now, setNow] = useState(() => Date.now());
  const [viewPaused, setViewPaused] = useState(false);
  const [frozenAt, setFrozenAt] = useState(() => Date.now());
  const [frozenTargets, setFrozenTargets] = useState<Target[] | null>(null);
  const [frozenSamplesByTarget, setFrozenSamplesByTarget] = useState<Record<
    string,
    Sample[]
  > | null>(null);
  const [frozenChartBucketsByTarget, setFrozenChartBucketsByTarget] = useState<
    Record<string, ChartBucket[]> | null
  >(null);
  const [formTarget, setFormTarget] = useState<Target | null | undefined>(
    undefined,
  );
  const [deleteTarget, setDeleteTarget] = useState<Target | null>(null);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [modalError, setModalError] = useState("");
  const [toast, setToast] = useState("");
  const [pageVisible, setPageVisible] = useState(
    () => document.visibilityState !== "hidden",
  );
  const serviceInstanceRef = useRef<string | null>(null);

  useEffect(() => {
    saveViewPreferences({ rangeMs, selectedTargetId, visibleTargets });
  }, [rangeMs, selectedTargetId, visibleTargets]);

  const checkServiceInstance = useCallback(async () => {
    try {
      const health = asRecord(await apiRequest("/api/health"));
      const instance = readString(health?.instance);
      if (!instance) return;
      const previous = serviceInstanceRef.current;
      serviceInstanceRef.current = instance;
      if (previous && previous !== instance) {
        setToast("采集服务已重启，历史数据正在重新累计");
      }
    } catch {
      // The EventSource error path already reports connectivity failures.
    }
  }, []);

  const ingestTargets = useCallback((incoming: Target[], replace = true) => {
    setTargets((previous) => {
      if (replace) return incoming;
      const map = new Map(previous.map((target) => [target.id, target]));
      incoming.forEach((target) => map.set(target.id, target));
      return Array.from(map.values());
    });
  }, []);

  const ingestSamples = useCallback((incoming: Sample[]) => {
    if (incoming.length === 0) return;
    setSamplesByTarget((previous) => mergeSamples(previous, incoming));
    setLastUpdated(Math.max(...incoming.map((sample) => sample.ts), Date.now()));
  }, []);

  const replaceSnapshotSamples = useCallback(
    (incoming: Sample[], targetIds: string[], snapshotAt: number) => {
      setSamplesByTarget((previous) => {
        const allowedTargets = new Set([
          ...targetIds,
          ...incoming.map((sample) => sample.target_id),
        ]);
        const latestByTarget = new Map<string, number>();
        incoming.forEach((sample) => {
          latestByTarget.set(
            sample.target_id,
            Math.max(latestByTarget.get(sample.target_id) ?? 0, sample.ts),
          );
        });
        const newerSamples = Object.entries(previous).flatMap(
          ([targetId, samples]) =>
            allowedTargets.has(targetId)
              ? samples.filter(
                  (sample) =>
                    sample.ts >
                    (latestByTarget.get(targetId) ?? snapshotAt),
                )
              : [],
        );
        return mergeSamples({}, [...incoming, ...newerSamples]);
      });
      const latestIncoming = incoming.reduce(
        (latest, sample) => Math.max(latest, sample.ts),
        snapshotAt,
      );
      setLastUpdated((previous) =>
        Math.max(previous ?? 0, latestIncoming),
      );
    },
    [],
  );

  const applySnapshot = useCallback(
    (payload: unknown) => {
      const parsed = parseSnapshot(payload);
      if (parsed.targets.length > 0) ingestTargets(parsed.targets, true);
      else {
        const root = asRecord(payload);
        if (
          root &&
          ("targets" in root ||
            "monitors" in root ||
            asRecord(root.data)?.targets !== undefined)
        ) {
          ingestTargets([], true);
        }
      }
      replaceSnapshotSamples(
        parsed.samples,
        parsed.targets.map((target) => target.id),
        parsed.updatedAt,
      );
      setChartBucketsByTarget(
        groupChartBuckets(
          parsed.buckets,
          parsed.targets.map((target) => target.id),
        ),
      );
    },
    [ingestTargets, replaceSnapshotSamples],
  );

  const refreshSnapshot = useCallback(
    async (quiet = false) => {
      if (!quiet) setConnection("connecting");
      try {
        const payload = await apiRequest("/api/snapshot");
        applySnapshot(payload);
        setLoadError("");
        setConnection("live");
      } catch (error) {
        const message =
          error instanceof Error ? error.message : "无法读取监控快照";
        setLoadError(message);
        setConnection("offline");
      } finally {
        setInitialLoading(false);
      }
    },
    [applySnapshot],
  );

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void refreshSnapshot();
    }, 0);
    return () => window.clearTimeout(timer);
  }, [refreshSnapshot]);

  useEffect(() => {
    if (!pageVisible) return;
    const timer = window.setInterval(() => {
      void refreshSnapshot(true);
    }, 300_000);
    return () => window.clearInterval(timer);
  }, [pageVisible, refreshSnapshot]);

  useEffect(() => {
    if (!pageVisible) return;
    const source = new EventSource(apiUrl("/api/events"), {
      withCredentials: false,
    });

    const parseEvent = (event: MessageEvent) => {
      try {
        return JSON.parse(event.data) as unknown;
      } catch {
        return event.data as unknown;
      }
    };

    const receiveSample = (payload: unknown) => {
      const record = asRecord(payload);
      const body =
        record?.sample ??
        record?.data ??
        record?.payload ??
        payload;
      const values = Array.isArray(body) ? body : [body];
      const normalized = values
        .map((sample) => normalizeSample(sample))
        .filter((sample): sample is Sample => sample !== null);
      ingestSamples(normalized);
      setConnection("live");
      setLoadError("");
    };

    const receiveTargets = (payload: unknown) => {
      const record = asRecord(payload);
      const body =
        record?.targets ??
        record?.data ??
        record?.payload ??
        payload;
      const values = Array.isArray(body) ? body : [body];
      const normalized = values
        .map((target, index) => normalizeTarget(target, index))
        .filter((target): target is Target => target !== null);
      if (normalized.length > 0 || Array.isArray(body)) {
        ingestTargets(normalized, Array.isArray(body));
      }
      setConnection("live");
    };

    const routePayload = (payload: unknown, hintedType = "") => {
      const record = asRecord(payload);
      const eventType = readString(
        hintedType,
        record?.type,
        record?.event,
        record?.kind,
      ).toLowerCase();
      if (eventType === "sample" || eventType === "probe") {
        receiveSample(payload);
      } else if (eventType === "targets" || eventType === "target") {
        receiveTargets(payload);
      } else if (eventType === "snapshot") {
        const body = record?.data ?? record?.snapshot ?? payload;
        applySnapshot(body);
        setConnection("live");
      } else if (eventType === "heartbeat" || eventType === "ping") {
        setConnection("live");
      } else if (
        record &&
        ("target_id" in record || "latency_ms" in record)
      ) {
        receiveSample(payload);
      } else if (record && ("targets" in record || "samples" in record)) {
        applySnapshot(payload);
        setConnection("live");
      }
    };

    source.onopen = () => {
      setConnection("live");
      setLoadError("");
      void checkServiceInstance();
    };
    source.onerror = () => {
      setConnection("offline");
      setLoadError(
        "本地监测核心暂时不可用，可能正在启动或重启；事件流会自动重连。",
      );
    };
    source.onmessage = (event) => routePayload(parseEvent(event));

    const listeners = ["sample", "snapshot", "targets", "heartbeat"] as const;
    const handlers = listeners.map((type) => {
      const handler = (event: Event) =>
        routePayload(parseEvent(event as MessageEvent), type);
      source.addEventListener(type, handler);
      return { type, handler };
    });

    return () => {
      handlers.forEach(({ type, handler }) =>
        source.removeEventListener(type, handler),
      );
      source.close();
    };
  }, [
    applySnapshot,
    checkServiceInstance,
    ingestSamples,
    ingestTargets,
    pageVisible,
  ]);

  useEffect(() => {
    if (!pageVisible) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [pageVisible]);

  useEffect(() => {
    const handleVisibilityChange = () => {
      const visible = document.visibilityState !== "hidden";
      if (visible) setNow(Date.now());
      setPageVisible(visible);
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    return () =>
      document.removeEventListener("visibilitychange", handleVisibilityChange);
  }, []);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(""), 3500);
    return () => window.clearTimeout(timer);
  }, [toast]);

  useEffect(() => {
    if (formTarget === undefined && !deleteTarget) return;
    const handleEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape" || saving || deleting) return;
      setFormTarget(undefined);
      setDeleteTarget(null);
      setModalError("");
    };
    window.addEventListener("keydown", handleEscape);
    return () => window.removeEventListener("keydown", handleEscape);
  }, [formTarget, deleteTarget, saving, deleting]);

  const chartAnchor = viewPaused ? frozenAt : now;
  const displaySamplesByTarget =
    viewPaused && frozenSamplesByTarget
      ? frozenSamplesByTarget
      : samplesByTarget;
  const displayChartBucketsByTarget =
    viewPaused && frozenChartBucketsByTarget
      ? frozenChartBucketsByTarget
      : chartBucketsByTarget;
  const displayTargets =
    viewPaused && frozenTargets ? frozenTargets : targets;
  const selectedTarget =
    displayTargets.find((target) => target.id === selectedTargetId) ?? null;
  const selectedRangeLabel =
    RANGE_OPTIONS.find((option) => option.value === rangeMs)?.label ?? "所选区间";

  const derivedStatsByTarget = useMemo(() => {
    const result: Record<string, TargetStats> = {};
    const windowStart = chartAnchor - rangeMs;
    displayTargets.forEach((target) => {
      const windowSamples = (displaySamplesByTarget[target.id] ?? []).filter(
        (sample) =>
          sample.bucket_ms === 0 &&
          sample.ts >= windowStart &&
          sample.ts <= chartAnchor,
      );
      const windowBuckets = (displayChartBucketsByTarget[target.id] ?? []).filter(
        (bucket) =>
          bucket.start_ms >= windowStart &&
          bucket.start_ms + bucket.duration_ms <= chartAnchor,
      );
      result[target.id] = deriveStats(
        windowSamples,
        windowBuckets,
        target.kind === "proxy_google",
      );
    });
    return result;
  }, [
    chartAnchor,
    displayChartBucketsByTarget,
    displaySamplesByTarget,
    rangeMs,
    displayTargets,
  ]);

  const targetStates = useMemo(() => {
    const result: Record<string, TargetState> = {};
    displayTargets.forEach((target) => {
      const visibleSamples = (displaySamplesByTarget[target.id] ?? []).filter(
        (sample) => sample.ts <= chartAnchor,
      );
      result[target.id] = getTargetState(
        target,
        visibleSamples,
        chartAnchor,
      );
    });
    return result;
  }, [chartAnchor, displaySamplesByTarget, displayTargets]);

  const latencySeries = useMemo(
    () => {
      const chartTargets = selectedTarget ? [selectedTarget] : displayTargets;
      const chartVisibility = selectedTarget
        ? { [selectedTarget.id]: true }
        : visibleTargets;
      return buildLatencySeries(
        chartTargets,
        displaySamplesByTarget,
        chartVisibility,
      );
    },
    [displaySamplesByTarget, displayTargets, selectedTarget, visibleTargets],
  );
  const lossEvents = useMemo(
    () => {
      const chartTargets = selectedTarget ? [selectedTarget] : displayTargets;
      const chartVisibility = selectedTarget
        ? { [selectedTarget.id]: true }
        : visibleTargets;
      return buildLossEvents(
        chartTargets,
        displaySamplesByTarget,
        chartVisibility,
      );
    },
    [displaySamplesByTarget, displayTargets, selectedTarget, visibleTargets],
  );

  const saveTarget = async (values: TargetFormValues) => {
    setSaving(true);
    setModalError("");
    const payload = {
      name: values.name,
      kind: values.kind,
      host: values.host,
      port: Number(values.port),
      proxy_host: values.kind === "proxy_google" ? values.proxy_host : "",
      proxy_port:
        values.kind === "proxy_google" ? Number(values.proxy_port) : 0,
      google_204_enabled:
        values.kind === "proxy_google" && values.google_204_enabled,
      interval_ms: Number(values.interval_ms),
      timeout_ms: Number(values.timeout_ms),
      enabled: values.enabled,
    };
    try {
      const editing = formTarget ?? null;
      const response = await apiRequest(
        editing
          ? "/api/targets/" + encodeURIComponent(editing.id)
          : "/api/targets",
        {
          method: editing ? "PUT" : "POST",
          body: JSON.stringify(payload),
        },
      );
      const record = asRecord(response);
      const normalized = normalizeTarget(
        record?.target ?? record?.data ?? response,
      );
      if (normalized) ingestTargets([normalized], false);
      await refreshSnapshot(true);
      setFormTarget(undefined);
      setToast(editing ? "目标设置已更新" : "监控目标已添加");
    } catch (error) {
      setModalError(
        error instanceof Error ? error.message : "保存目标时发生错误",
      );
    } finally {
      setSaving(false);
    }
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    setModalError("");
    try {
      await apiRequest(
        "/api/targets/" + encodeURIComponent(deleteTarget.id),
        { method: "DELETE" },
      );
      setTargets((previous) =>
        previous.filter((target) => target.id !== deleteTarget.id),
      );
      setDeleteTarget(null);
      setToast("监控目标已删除");
    } catch (error) {
      setModalError(
        error instanceof Error ? error.message : "删除目标时发生错误",
      );
    } finally {
      setDeleting(false);
    }
  };

  const toggleTarget = async (target: Target) => {
    const next = { ...target, enabled: !target.enabled };
    setTargets((previous) =>
      previous.map((item) => (item.id === target.id ? next : item)),
    );
    try {
      await apiRequest("/api/targets/" + encodeURIComponent(target.id), {
        method: "PUT",
        body: JSON.stringify({
          name: next.name,
          kind: next.kind,
          host: next.host,
          port: next.port,
          proxy_host: next.proxy_host,
          proxy_port: next.proxy_port,
          google_204_enabled: next.google_204_enabled,
          interval_ms: next.interval_ms,
          timeout_ms: next.timeout_ms,
          enabled: next.enabled,
        }),
      });
      setToast(next.enabled ? "已恢复目标监控" : "已暂停目标监控");
    } catch (error) {
      setTargets((previous) =>
        previous.map((item) => (item.id === target.id ? target : item)),
      );
      setToast(error instanceof Error ? error.message : "更新状态失败");
    }
  };

  const resumeView = () => {
    setViewPaused(false);
    setFrozenTargets(null);
    setFrozenSamplesByTarget(null);
    setFrozenChartBucketsByTarget(null);
  };

  const toggleViewPause = () => {
    if (viewPaused) {
      resumeView();
      return;
    }
    setFrozenAt(Date.now());
    setFrozenTargets(targets);
    setFrozenSamplesByTarget(samplesByTarget);
    setFrozenChartBucketsByTarget(chartBucketsByTarget);
    setViewPaused(true);
  };

  const openTargetEditor = (target: Target | null) => {
    if (viewPaused) resumeView();
    setModalError("");
    setFormTarget(target);
  };

  const openDeleteTarget = (target: Target) => {
    if (viewPaused) resumeView();
    setModalError("");
    setDeleteTarget(target);
  };

  const selectedStats = selectedTarget
    ? (derivedStatsByTarget[selectedTarget.id] ?? EMPTY_STATS)
    : EMPTY_STATS;
  const selectedState = selectedTarget
    ? (targetStates[selectedTarget.id] ??
      getTargetState(selectedTarget, [], chartAnchor))
    : null;
  const selectedSamples = selectedTarget
    ? (displaySamplesByTarget[selectedTarget.id] ?? []).filter(
        (sample) => sample.ts <= chartAnchor,
      )
    : [];
  const selectedLatest = selectedSamples[selectedSamples.length - 1];
  const selectedLiveTarget = selectedTarget
    ? (targets.find((target) => target.id === selectedTarget.id) ?? selectedTarget)
    : null;

  const connectionCopy =
    connection === "live"
      ? "实时采集中"
      : connection === "connecting"
        ? "正在连接"
        : "连接已中断";

  return (
    <div className="desktop-app">
      <DesktopSidebar
        targets={displayTargets}
        statsByTarget={derivedStatsByTarget}
        statesByTarget={targetStates}
        selectedTargetId={selectedTarget?.id ?? null}
        visibleTargets={visibleTargets}
        connection={connection}
        connectionCopy={connectionCopy}
        lastUpdated={lastUpdated}
        now={now}
        onSelect={setSelectedTargetId}
        onToggleChart={(targetId) =>
          setVisibleTargets((previous) => ({
            ...previous,
            [targetId]: previous[targetId] === false,
          }))
        }
      />

      <div className="workspace-shell">
        <header className="workspace-toolbar">
          <div className="workspace-title">
            <span>
              {selectedTarget
                ? selectedTarget.kind === "proxy_google"
                  ? "节点探测详情"
                  : "TCP 监测详情"
                : "监测总览"}
            </span>
            <div>
              <h1>{selectedTarget?.name ?? "全部目标"}</h1>
              {selectedState && (
                <b className={`status-badge tone-${selectedState.tone}`}>
                  {selectedState.label}
                </b>
              )}
            </div>
            <p>
              {selectedTarget
                ? targetEndpointText(selectedTarget)
                : "比较全部 TCP 与代理节点的实时连接质量"}
            </p>
          </div>

          <div className="workspace-actions">
            {viewPaused && (
              <button
                className="frozen-badge"
                type="button"
                onClick={toggleViewPause}
              >
                定格于 {formatClock(frozenAt)}
              </button>
            )}
            <button
              className="button button-secondary compact"
              type="button"
              onClick={toggleViewPause}
              aria-pressed={viewPaused}
            >
              <span aria-hidden="true">{viewPaused ? "▶" : "Ⅱ"}</span>
              {viewPaused ? "继续实时" : "暂停视图"}
            </button>
            <button
              className="button button-primary compact"
              type="button"
              onClick={() => openTargetEditor(null)}
            >
              <span aria-hidden="true">＋</span>
              添加目标
            </button>
          </div>
        </header>

        <main
          id="main-content"
          className={
            "workspace-main" + (selectedTarget ? " target-selected" : "")
          }
        >
        {connection === "offline" && (
          <div className="service-banner" role="alert">
            <span className="banner-mark" aria-hidden="true">
              !
            </span>
            <div>
              <strong>采集服务连接中断</strong>
              <p>
                当前页面中的数据暂时保留，正在等待本地监测核心恢复。
                {loadError ? " " + loadError : ""}
              </p>
            </div>
            <button
              className="button button-secondary compact"
              type="button"
              onClick={() => refreshSnapshot()}
            >
              立即重试
            </button>
          </div>
        )}

        <section className="chart-panel" aria-labelledby="trend-title">
          <div className="panel-head">
            <div>
              <h2 id="trend-title">
                {selectedTarget ? "目标延迟趋势" : "综合延迟趋势"}
              </h2>
              <p>
                {selectedTarget
                  ? "查看当前目标在所选时间范围内的连接表现"
                  : "比较所有已显示目标的连接延迟与丢失事件"}
              </p>
            </div>
            <div className="range-control" aria-label="图表时间范围">
              {RANGE_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={rangeMs === option.value ? "active" : ""}
                  aria-pressed={rangeMs === option.value}
                  onClick={() => setRangeMs(option.value)}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </div>

          <div className="chart-legend" aria-label="显示或隐藏目标折线">
            {selectedTarget ? (
              <span className="legend-item selected-context">
                <i
                  aria-hidden="true"
                  style={{
                    backgroundColor: colorForIndex(
                      Math.max(
                        0,
                        displayTargets.findIndex(
                          (target) => target.id === selectedTarget.id,
                        ),
                      ),
                    ),
                  }}
                />
                {selectedTarget.name}
              </span>
            ) : displayTargets.length === 0 ? (
              <span className="legend-empty">添加目标后，折线图例将在此显示</span>
            ) : (
              displayTargets.map((target, index) => (
                <button
                  key={target.id}
                  type="button"
                  className={
                    "legend-item" +
                    (visibleTargets[target.id] === false ? " muted" : "")
                  }
                  aria-pressed={visibleTargets[target.id] !== false}
                  onClick={() =>
                    setVisibleTargets((previous) => ({
                      ...previous,
                      [target.id]: previous[target.id] === false,
                    }))
                  }
                >
                  <i
                    aria-hidden="true"
                    style={{ backgroundColor: colorForIndex(index) }}
                  />
                  {target.name}
                </button>
              ))
            )}
          </div>

          <div className="chart-block">
            <div className="chart-title-row">
              <div>
                <h3>探测延迟</h3>
                <p>
                  节点显示 TLS 完成，直连显示 TCP 建连；橙色圆点表示延迟尖峰，红色三角表示超时或失败
                </p>
              </div>
              <span>毫秒</span>
            </div>
            <CanvasChart
              series={latencySeries}
              events={lossEvents}
              windowMs={rangeMs}
              anchorTime={chartAnchor}
            />
          </div>
        </section>

        <section
          className={
            "target-workspace-panel" +
            (selectedTarget ? " detail-mode" : " overview-mode")
          }
          aria-labelledby="targets-title"
        >
          <div className="target-workspace-head">
            <div>
              <h2 id="targets-title">
                {selectedTarget ? "目标详情" : "目标概览"}
              </h2>
              <p>
                {selectedTarget
                  ? selectedState?.detail
                  : displayTargets.length +
                    " 个目标 · 统计范围 " +
                    selectedRangeLabel}
              </p>
            </div>
            {selectedTarget && (
              <button
                className="button button-secondary compact"
                type="button"
                onClick={() => setSelectedTargetId(null)}
              >
                ← 返回全部目标
              </button>
            )}
          </div>

          {initialLoading && displayTargets.length === 0 ? (
            <div className="empty-state" aria-live="polite">
              <span className="empty-radar loading-radar" aria-hidden="true">
                <i />
              </span>
              <h3>正在读取监控快照</h3>
              <p>正在连接本地采集服务，请稍候。</p>
            </div>
          ) : displayTargets.length === 0 && loadError ? (
            <div className="empty-state">
              <span className="empty-radar error-radar" aria-hidden="true">
                <i>!</i>
              </span>
              <h3>暂时无法读取目标</h3>
              <p>{loadError}</p>
              <button
                className="button button-primary"
                type="button"
                onClick={() => refreshSnapshot()}
              >
                重新连接
              </button>
            </div>
          ) : displayTargets.length === 0 ? (
            <div className="empty-state">
              <span className="empty-radar" aria-hidden="true">
                <i>＋</i>
              </span>
              <h3>还没有监控目标</h3>
              <p>添加直接 TCP 目标，或填写本地 SOCKS5 端口开始节点探测。</p>
              <button
                className="button button-primary"
                type="button"
                onClick={() => openTargetEditor(null)}
              >
                ＋ 添加第一个目标
              </button>
              <small>节点默认验证 Google TLS，可选继续验证 HTTP 204</small>
            </div>
          ) : selectedTarget && selectedState && selectedLiveTarget ? (
            <div className="target-detail">
              <div className="target-detail-head">
                <div className="target-detail-identity">
                  <span
                    className={"target-detail-accent tone-" + selectedState.tone}
                    style={{
                      borderColor: colorForIndex(
                        Math.max(
                          0,
                          displayTargets.findIndex(
                            (target) => target.id === selectedTarget.id,
                          ),
                        ),
                      ),
                    }}
                    aria-hidden="true"
                  />
                  <div>
                    <div>
                      <strong>{selectedTarget.name}</strong>
                      <span
                        className={"status-badge tone-" + selectedState.tone}
                      >
                        {selectedState.label}
                      </span>
                    </div>
                    <p>{targetEndpointText(selectedTarget)}</p>
                    <small>
                      {selectedLatest
                        ? "最近探测 " +
                          formatRelative(selectedLatest.ts, chartAnchor)
                        : "等待首次探测"}
                    </small>
                  </div>
                </div>
                <div className="target-detail-actions">
                  <button
                    className="button button-secondary compact"
                    type="button"
                    onClick={() => {
                      if (viewPaused) resumeView();
                      void toggleTarget(selectedLiveTarget);
                    }}
                  >
                    {selectedTarget.enabled ? "暂停监测" : "恢复监测"}
                  </button>
                  <button
                    className="button button-secondary compact"
                    type="button"
                    onClick={() => openTargetEditor(selectedLiveTarget)}
                  >
                    编辑
                  </button>
                  <button
                    className="button button-secondary compact danger-outline"
                    type="button"
                    onClick={() => openDeleteTarget(selectedLiveTarget)}
                  >
                    删除
                  </button>
                </div>
              </div>

              <div className="target-detail-body">
                <div className="detail-current">
                  <span>
                    {selectedTarget.kind === "proxy_google"
                      ? selectedTarget.google_204_enabled
                        ? "Google 204"
                        : "Google TLS 就绪"
                      : "当前 TCP 延迟"}
                  </span>
                  <strong>{formatMs(selectedStats.current_ms, 1)}</strong>
                  <p>{selectedState.detail}</p>
                </div>

                <dl className="detail-metrics">
                  {selectedTarget.kind === "proxy_google" ? (
                    <>
                      <MetricTile
                        label="平均 TLS 完成"
                        value={formatMs(selectedStats.tls_average_ms)}
                        title={
                          selectedRangeLabel +
                          "内有效 TLS 完成延迟的算术平均值"
                        }
                      />
                      <MetricTile
                        label="TLS 首包"
                        value={formatMs(
                          selectedStats.remote_first_byte_current_ms,
                        )}
                      />
                      <MetricTile
                        label="TLS 完成"
                        value={formatMs(selectedStats.tls_current_ms)}
                      />
                      <MetricTile
                        label={
                          selectedTarget.google_204_enabled
                            ? "204 P95" +
                              (selectedStats.p95_approximate ? "≈" : "")
                            : "TLS P95" +
                              (selectedStats.p95_approximate ? "≈" : "")
                        }
                        value={formatMs(selectedStats.p95_ms)}
                        title={
                          selectedStats.p95_approximate
                            ? "包含分钟摘要；延迟量化相对误差不超过 2%"
                            : undefined
                        }
                      />
                      <MetricTile
                        label="波动率"
                        value={formatPercent(selectedStats.volatility_rate)}
                        title={
                          selectedRangeLabel +
                          "内 TLS 完成延迟的标准差 ÷ 平均值"
                        }
                      />
                      <MetricTile
                        label="丢包率"
                        value={formatPercent(
                          selectedStats.estimated_loss_rate,
                        )}
                        lossRangeLabel={selectedRangeLabel}
                      />
                    </>
                  ) : (
                    <>
                      <MetricTile
                        label="平均延迟"
                        value={formatMs(selectedStats.average_ms)}
                        title={
                          selectedRangeLabel +
                          "内有效 TCP 建连延迟的算术平均值"
                        }
                      />
                      <MetricTile
                        label={
                          "P95 延迟" +
                          (selectedStats.p95_approximate ? "≈" : "")
                        }
                        value={formatMs(selectedStats.p95_ms)}
                        title={
                          selectedStats.p95_approximate
                            ? "包含分钟摘要；延迟量化相对误差不超过 2%"
                            : undefined
                        }
                      />
                      <MetricTile
                        label="成功率"
                        value={formatPercent(selectedStats.success_rate)}
                      />
                      <MetricTile
                        label="拒绝率"
                        value={formatPercent(selectedStats.refused_rate)}
                      />
                      <MetricTile
                        label="波动率"
                        value={formatPercent(selectedStats.volatility_rate)}
                        title={
                          selectedRangeLabel +
                          "内有效 TCP 建连延迟的标准差 ÷ 平均值"
                        }
                      />
                      <MetricTile
                        label="丢包率"
                        value={formatPercent(
                          selectedStats.estimated_loss_rate,
                        )}
                        lossRangeLabel={selectedRangeLabel}
                      />
                    </>
                  )}
                </dl>
              </div>
            </div>
          ) : (
            <div className="target-table" role="table" aria-label="监控目标概览">
              <div className="target-table-header" role="row">
                <span role="columnheader">目标</span>
                <span role="columnheader">当前</span>
                <span role="columnheader">平均</span>
                <span role="columnheader">P95</span>
                <span role="columnheader">丢包率</span>
                <span role="columnheader">波动率</span>
                <span role="columnheader" aria-label="操作" />
              </div>
              <div className="target-table-body">
                {displayTargets.map((target, index) => {
                  const stats =
                    derivedStatsByTarget[target.id] ?? EMPTY_STATS;
                  const state =
                    targetStates[target.id] ??
                    getTargetState(target, [], chartAnchor);
                  const liveTarget =
                    targets.find((item) => item.id === target.id) ?? target;
                  const average =
                    target.kind === "proxy_google"
                      ? stats.tls_average_ms
                      : stats.average_ms;
                  return (
                    <div
                      className="target-table-row"
                      role="row"
                      key={target.id}
                    >
                      <div className="target-table-primary" role="cell">
                        <button
                          type="button"
                          onClick={() => setSelectedTargetId(target.id)}
                        >
                          <i
                            className={"target-dot tone-" + state.tone}
                            style={{
                              borderColor: colorForIndex(index),
                            }}
                            aria-hidden="true"
                          />
                          <span>
                            <strong>{target.name}</strong>
                            <small>{targetEndpointText(target)}</small>
                          </span>
                        </button>
                        <span className={"status-badge tone-" + state.tone}>
                          {state.label}
                        </span>
                      </div>
                      <div className="target-table-value" role="cell">
                        <span>当前</span>
                        <strong>{formatMs(stats.current_ms, 1)}</strong>
                      </div>
                      <div className="target-table-value" role="cell">
                        <span>平均</span>
                        <strong>{formatMs(average)}</strong>
                      </div>
                      <div className="target-table-value" role="cell">
                        <span>P95</span>
                        <strong>
                          {formatMs(stats.p95_ms)}
                          {stats.p95_approximate ? "≈" : ""}
                        </strong>
                      </div>
                      <div className="target-table-value" role="cell">
                        <span>丢包率</span>
                        <strong>
                          {formatPercent(stats.estimated_loss_rate)}
                        </strong>
                      </div>
                      <div className="target-table-value" role="cell">
                        <span>波动率</span>
                        <strong>{formatPercent(stats.volatility_rate)}</strong>
                      </div>
                      <div className="target-table-actions" role="cell">
                        <TargetActionMenu
                          target={liveTarget}
                          onEdit={openTargetEditor}
                          onToggle={(item) => {
                            if (viewPaused) resumeView();
                            void toggleTarget(item);
                          }}
                          onDelete={openDeleteTarget}
                        />
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </section>
        </main>

        <footer className="desktop-statusbar">
          <span className={"statusbar-connection connection-" + connection}>
            <i aria-hidden="true" />
            {connectionCopy}
          </span>
          <span>
            {viewPaused
              ? "视图已暂停，后台采集仍在继续"
              : "后台监测运行中"}
          </span>
          <span>统计范围 {selectedRangeLabel}</span>
          <span>{displayTargets.length} 个目标</span>
        </footer>
      </div>

      {formTarget !== undefined && (
        <TargetModal
          key={formTarget?.id ?? "new"}
          target={formTarget}
          saving={saving}
          error={modalError}
          onClose={() => {
            setFormTarget(undefined);
            setModalError("");
          }}
          onSubmit={saveTarget}
        />
      )}

      {deleteTarget && (
        <DeleteModal
          target={deleteTarget}
          deleting={deleting}
          error={modalError}
          onClose={() => {
            setDeleteTarget(null);
            setModalError("");
          }}
          onConfirm={confirmDelete}
        />
      )}

      {toast && (
        <div className="toast" role="status" aria-live="polite">
          <span aria-hidden="true">✓</span>
          {toast}
        </div>
      )}
    </div>
  );
}

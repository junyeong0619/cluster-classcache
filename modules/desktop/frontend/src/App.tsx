import { useEffect, useMemo, useRef, useState } from 'react';
import './App.css';
import {
  Diagnose,
  ListClassCaches,
  ListArchives,
  SampleSavings,
  PodStats,
} from '../wailsjs/go/main/App';
import { main } from '../wailsjs/go/models';

const POLL_MS = 1500;
const HISTORY_LEN = 80;
const TABLE_CAP = 200;  // cap visible table rows; the rest collapse into "+ N more"

// ─── Synthetic demo data (cluster-scale) ─────────────────────────────
// Production-ish numbers: 60 nodes, 1000 JVM pods sharing 12 distinct
// archives across 80 ClassCache CRs. Time-series values drift via sin
// waves so charts and packet flows look live.
const DEMO = {
  NODES: 60,
  PODS: 1000,
  CCS: 80,
  ARCHIVES: 12,
};

function makeDemo(tick: number) {
  const wave  = (period: number, amp: number, off: number) => Math.round(Math.sin(tick / period) * amp + off);
  const waveF = (period: number, amp: number, off: number) => Math.sin(tick / period) * amp + off;

  // Stable node endpoints — 60 IPs across 4 /16 subnets, port 7777
  const nodes = Array.from({ length: DEMO.NODES }, (_, i) =>
    `10.244.${Math.floor(i / 15) + 1}.${(i % 15) * 3 + 5}:7777`);

  // 12 deterministic archive keys (16-hex). In production these are sha256
  // truncations of (app.jar || agent.jar || jvm || arch || profile).
  const archiveKeys = [
    'e3b0c44298fc1c14', '7c5d2afebf4729a1', 'a91f37c2b5e4d018',
    '2cd4f8e73a9d6c12', '5e8b41ff6c7a2d09', '8f1e3b56d4c9a872',
    'b3f7e29ac8d51604', '4f1b39c0e785a2d6', 'c92a47f1b6308e5d',
    '1e6b08c79a4f2dab', '74d3b1ea5c829f06', 'a5fc826b390d4e71',
  ];
  const sizes  = [38, 41, 22, 64, 47, 28, 33, 51, 19, 44, 36, 25];
  const jvmLabels = ['OpenJDK 22.0.1', 'OpenJDK 22.0.2', 'OpenJDK 21.0.4'];

  const archives: Archive[] = archiveKeys.map((key, i) => {
    const peerCount = 12 + (i * 5) % 28; // 12..40 peers
    const peerEndpoints: string[] = [];
    for (let j = 0; j < peerCount; j++) {
      peerEndpoints.push(nodes[(i * 7 + j * 3) % DEMO.NODES]);
    }
    return {
      key,
      sizeBytes: sizes[i] * 1024 * 1024,
      peerEndpoints,
      jvm:  jvmLabels[i % jvmLabels.length],
      arch: i % 5 === 0 ? 'amd64' : 'arm64',
    } as Archive;
  });

  const phases     = ['Ready', 'Ready', 'Ready', 'Ready', 'Ready', 'Ready', 'Ready', 'WorkloadPatched', 'PrimerReady', 'Pending', 'Failed'];
  const namespaces = ['demo', 'shop', 'iam', 'billing', 'inventory', 'analytics', 'notifications', 'reporting'];
  const profiles   = ['default', 'jvm22', 'jvm22', 'low-mem', 'high-throughput'];

  const ccs: CC[] = Array.from({ length: DEMO.CCS }, (_, i) => {
    const phase = phases[(i + 3) % phases.length];
    const ns    = namespaces[i % namespaces.length];
    const ar    = archives[i % archives.length];
    const noKey = phase === 'Pending' || phase === 'PrimerReady' || phase === 'Failed';
    return {
      name: `svc-${String(i + 1).padStart(3, '0')}`,
      namespace: ns,
      workloadName: `svc-${String(i + 1).padStart(3, '0')}`,
      profile: profiles[i % profiles.length],
      phase,
      archiveKey: noKey ? '' : ar.key,
    } as CC;
  });

  const pods: Pod[] = Array.from({ length: DEMO.PODS }, (_, i) => {
    const cc      = ccs[i % DEMO.CCS];
    const nodeIdx = i % DEMO.NODES;
    return {
      namespace: cc.namespace,
      name: `${cc.name}-${String(i).padStart(4, '0')}`,
      node: `mc-1-worker-${String(nodeIdx + 1).padStart(2, '0')}`,
      cpuMilli: Math.max(20, 80 + wave(5 + (i % 7), 30, 20)),
      memMiB:   Math.max(40, 120 + wave(7 + (i % 11), 40, 30)),
    } as Pod;
  });

  // Aggregate memory math @ scale:
  // ~47 MiB Rss per JVM × 1000 JVMs = ~46 GiB cluster-wide Rss.
  // ~16.7 JVMs share each archive page on a given node, so Pss = Rss/16.7
  // ≈ 2.8 GiB. Saved = Rss − Pss ≈ 43 GiB.
  const jvms = DEMO.PODS;
  const rss  = Math.round(47000 * 1024 + waveF(7, 1500 * 1024, 0));
  const pss  = Math.round(rss / 16.7);
  const sc   = Math.round(rss * 0.963 + waveF(9, 40  * 1024, 0)); // ~96.3% Shared_Clean
  const pc   = Math.round(rss * 0.024 + waveF(11, 6  * 1024, 0));
  const pd   = Math.round(rss * 0.008 + waveF(13, 2  * 1024, 0));
  const sd   = Math.max(0, rss - sc - pc - pd);
  const totalSize = jvms * 49 * 1024; // 49 MiB VMA × 1000 = ~48 GiB

  const savings: Savings = {
    timestamp: Math.floor(Date.now() / 1000),
    totalSizeKiB: totalSize,
    totalRssKiB: rss,
    totalPssKiB: pss,
    savedKiB: rss - pss,
    sharedCleanKiB: sc,
    sharedDirtyKiB: sd,
    privateCleanKiB: pc,
    privateDirtyKiB: pd,
    jvms,
  } as Savings;

  return { ccs, archives, savings, pods };
}

type CC = main.ClassCacheSummary;
type Archive = main.ArchiveSummary;
type Savings = main.SavingsSnapshot;
type Diag = main.Diag;
type Pod = main.PodStat;
type View = 'classcaches' | 'archives' | 'topology' | 'pods' | 'events' | 'settings';

// ─── Formatters ───────────────────────────────────────────────────────
function fmtKiB(kib: number): { v: string; unit: string } {
  if (kib < 1024) return { v: String(kib), unit: 'KB' };
  if (kib < 1024 * 1024) return { v: (kib / 1024).toFixed(1), unit: 'MB' };
  return { v: (kib / 1024 / 1024).toFixed(2), unit: 'GB' };
}
function fmtBytes(b: number) { return fmtKiB(b / 1024); }
function fmtClock() { return new Date().toISOString().slice(11, 19); }
function tabular(v: string): string { return v.replace(/\./g, '·'); }  // typographic flourish

// Smooth animated number — runs the hero counter forward when target changes.
function useAnimatedNumber(target: number, duration = 600): number {
  const [val, setVal] = useState(target);
  const fromRef = useRef(target);
  const startRef = useRef<number>(0);
  const rafRef = useRef<number>(0);
  useEffect(() => {
    fromRef.current = val;
    startRef.current = performance.now();
    const step = (t: number) => {
      const k = Math.min(1, (t - startRef.current) / duration);
      const eased = 1 - Math.pow(1 - k, 3);
      setVal(fromRef.current + (target - fromRef.current) * eased);
      if (k < 1) rafRef.current = requestAnimationFrame(step);
    };
    rafRef.current = requestAnimationFrame(step);
    return () => cancelAnimationFrame(rafRef.current);
  }, [target]);
  return val;
}

// Force a re-render every 1s so the UTC clock in the topstrip ticks.
function useTick(intervalMs: number) {
  const [, setN] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setN((n) => n + 1), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
}

// ─── Oscilloscope chart ──────────────────────────────────────────────
// Engineering-style line chart: explicit horizontal gridlines, axis tick
// labels in the gutter, a tiny dot at every sample, and a pulsing dot at
// the latest sample (the live "now" marker).
function Oscilloscope({ data, label, value, unit, peakLabel, color }: {
  data: number[]; label: string; value: string; unit?: string; peakLabel?: string; color: string;
}) {
  const W = 700, H = 140;
  const padL = 40, padR = 12, padT = 14, padB = 22;
  const innerW = W - padL - padR;
  const innerH = H - padT - padB;
  const peak = Math.max(1, ...(data.length ? data : [1]));
  const step = innerW / Math.max(1, data.length - 1);
  const pts = data.map((v, i) => [padL + i * step, padT + innerH - (v / peak) * innerH] as const);
  const linePts = pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' ');
  const last = pts.length ? pts[pts.length - 1] : null;

  // Pretty axis label for a [0..1] fraction of peak (top=1, bottom=0)
  const axisAt = (frac: number) => {
    const v = peak * (1 - frac);
    if (v < 1024) return Math.round(v).toString();
    if (v < 1024 * 1024) return (v / 1024).toFixed(1) + 'k';
    return (v / 1024 / 1024).toFixed(1) + 'M';
  };
  const grid = [0, 0.25, 0.5, 0.75, 1];

  return (
    <div className="osc">
      {(label || value) && (
        <div className="osc-head">
          <span className="osc-label">{label}</span>
          <span className="osc-value">
            {value}
            {unit && <span className="unit">{unit}</span>}
            {peakLabel && <span className="peak">peak {peakLabel}</span>}
          </span>
        </div>
      )}
      <svg className="osc-svg" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
        <g className="osc-grid">
          {grid.map((p) => (
            <line key={p} x1={padL} x2={W - padR} y1={padT + p * innerH} y2={padT + p * innerH} />
          ))}
        </g>
        <g className="osc-axis">
          {grid.map((p) => (
            <text key={p} x={padL - 8} y={padT + p * innerH + 3} textAnchor="end">{axisAt(p)}</text>
          ))}
        </g>
        {data.length > 1 && <polyline className="osc-line" points={linePts} stroke={color} />}
        {pts.map(([x, y], i) => (
          <circle key={i} cx={x} cy={y} r={1.2} fill={color} opacity={0.55} />
        ))}
        {last && (
          <>
            <line x1={last[0]} x2={last[0]} y1={padT} y2={padT + innerH} stroke={color} strokeWidth={0.4} strokeDasharray="2 3" opacity={0.5} />
            <circle className="osc-dot" cx={last[0]} cy={last[1]} r={3.2} fill={color} />
          </>
        )}
        {data.length === 0 && (
          <text x={W / 2} y={H / 2} textAnchor="middle" fill="#5a6b85" fontSize="10" fontFamily="'JetBrains Mono', monospace">collecting…</text>
        )}
      </svg>
    </div>
  );
}

// ─── mmap instrument ────────────────────────────────────────────────
// Big italic hit-rate gauge + spectrum bar with engineering tick marks
// labelled 0…100 %. Each smaps state gets its own legend row.
function MmapInstrument({ savings }: { savings: Savings | null }) {
  const s = savings;
  const sc = s?.sharedCleanKiB || 0;
  const sd = s?.sharedDirtyKiB || 0;
  const pc = s?.privateCleanKiB || 0;
  const pd = s?.privateDirtyKiB || 0;
  const rss = sc + sd + pc + pd;
  const size = s?.totalSizeKiB || 0;
  const denom = Math.max(1, rss);
  const pct = (x: number) => ((x / denom) * 100).toFixed(1) + '%';
  const hitRate = rss === 0 ? 0 : ((sc + sd) / rss) * 100;
  const k = (v: number) => { const r = fmtKiB(v); return `${r.v} ${r.unit}`; };

  return (
    <div className="mmap">
      <div className="mmap-left">
        <div className="mmap-cap">
          mmap PAGE STATE · across <b>{s?.jvms ?? 0}</b> JVM · Σ Size <b>{k(size)}</b> · Σ Rss <b>{k(rss)}</b>
        </div>
        <div className="mmap-bar-wrap">
          <div className="mmap-ticks">
            {[0, 25, 50, 75, 100].map((p) => <span key={p} className="mmap-tick">{p}%</span>)}
          </div>
          <div className="mmap-bar">
            {sc > 0 && <div className="mmap-seg sc" style={{ width: pct(sc) }} title={`Shared_Clean ${k(sc)}`} />}
            {sd > 0 && <div className="mmap-seg sd" style={{ width: pct(sd) }} title={`Shared_Dirty ${k(sd)}`} />}
            {pc > 0 && <div className="mmap-seg pc" style={{ width: pct(pc) }} title={`Private_Clean ${k(pc)}`} />}
            {pd > 0 && <div className="mmap-seg pd" style={{ width: pct(pd) }} title={`Private_Dirty ${k(pd)}`} />}
          </div>
        </div>
        <div className="mmap-legend">
          <div className="row"><span className="sw sc" /><span className="label">Shared_Clean<span className="tag">HIT</span></span><span className="val">{k(sc)}</span></div>
          <div className="row"><span className="sw sd" /><span className="label">Shared_Dirty</span><span className="val">{k(sd)}</span></div>
          <div className="row"><span className="sw pc" /><span className="label">Private_Clean<span className="tag">MISS</span></span><span className="val">{k(pc)}</span></div>
          <div className="row"><span className="sw pd" /><span className="label">Private_Dirty<span className="tag">MISS</span></span><span className="val">{k(pd)}</span></div>
        </div>
      </div>
      <div className="mmap-gauge">
        <div className="num">{hitRate.toFixed(1)}<span className="pct">%</span></div>
        <div className="lbl">page-cache hit rate</div>
      </div>
    </div>
  );
}

// ─── Engineering-schematic topology ─────────────────────────────────
// Peers arranged on a ring; each peer is a labelled engineering box with
// corner brackets. Pairs of peers that share an archive get a cobalt edge
// with a triangular "packet" sliding along it (animateMotion). Center has
// a dashed crosshair marker.
function SchemTopology({ archives, savings }: { archives: Archive[]; savings: Savings | null }) {
  const W = 900, H = 540;
  const cx = W / 2, cy = H / 2;

  const peers = useMemo(() => {
    const s = new Set<string>();
    for (const a of archives) for (const p of (a.peerEndpoints || [])) s.add(p);
    return Array.from(s).sort();
  }, [archives]);

  // Scale node box + ring radius based on cluster size so 60 nodes pack neatly.
  const big = peers.length > 16;
  const NW = big ? 64 : 108;
  const NH = big ? 22 : 36;
  const B  = big ? 3 : 4;
  const radius = Math.min(W, H) * (big ? 0.40 : 0.34);

  const nodes = peers.map((p, i) => {
    const ang = -Math.PI / 2 + (2 * Math.PI * i) / Math.max(1, peers.length);
    return {
      peer: p,
      x: cx + radius * Math.cos(ang),
      y: cy + radius * Math.sin(ang),
      num: String(i + 1).padStart(2, '0'),
    };
  });
  const byPeer = new Map(nodes.map((n) => [n.peer, n]));

  // Cap edges so a 60-node ring doesn't render thousands of crisscrossing lines.
  // Keep edges from the largest archives first (those are the "hot" ones).
  const edges = useMemo(() => {
    const sortedArchives = [...archives].sort((a, b) => (b.peerEndpoints?.length || 0) - (a.peerEndpoints?.length || 0));
    const seen = new Set<string>();
    const out: { a: string; b: string }[] = [];
    const MAX_EDGES = big ? 90 : 30;
    outer: for (const ar of sortedArchives) {
      const ps = ar.peerEndpoints || [];
      for (let i = 0; i < ps.length; i++) {
        for (let j = i + 1; j < ps.length; j++) {
          const k = ps[i] < ps[j] ? ps[i] + '|' + ps[j] : ps[j] + '|' + ps[i];
          if (!seen.has(k)) { seen.add(k); out.push({ a: ps[i], b: ps[j] }); }
          if (out.length >= MAX_EDGES) break outer;
        }
      }
    }
    return out;
  }, [archives, big]);
  const MAX_PACKETS = big ? 18 : 30;

  return (
    <>
      <div className="schem-title">cluster topology <small>live</small></div>
      <div className="schem-meta">
        nodes <b>{nodes.length}</b> · edges <b>{edges.length}</b><br />
        archives <b>{archives.length}</b> · jvms <b>{savings?.jvms ?? 0}</b>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`}>
        {/* dashed orbit guide */}
        <circle cx={cx} cy={cy} r={radius} fill="none" stroke="rgba(15,35,80,0.08)" strokeDasharray="2 6" />

        {/* edges + flowing packets (packets cap to MAX_PACKETS for perf) */}
        {edges.map((e, i) => {
          const A = byPeer.get(e.a), B2 = byPeer.get(e.b);
          if (!A || !B2) return null;
          const dur = 2.4 + (i % 3) * 0.6;
          const animate = i < MAX_PACKETS;
          return (
            <g key={e.a + '-' + e.b}>
              <line x1={A.x} y1={A.y} x2={B2.x} y2={B2.y}
                    stroke={big ? 'rgba(37,99,235,0.22)' : 'rgba(37,99,235,0.32)'}
                    strokeWidth={big ? 0.6 : 0.9} />
              {animate && (
                <polygon points="0,-3 5,0 0,3" fill="#2563eb">
                  <animateMotion dur={`${dur}s`} begin={`${(i * 0.25).toFixed(2)}s`} repeatCount="indefinite" rotate="auto"
                    path={`M${A.x},${A.y} L${B2.x},${B2.y}`} />
                </polygon>
              )}
            </g>
          );
        })}

        {/* center crosshair marker */}
        <g transform={`translate(${cx},${cy})`}>
          <circle r="2.5" fill="#0c2754" />
          <line x1="-14" x2="-6" y1="0" y2="0" stroke="#0c2754" strokeWidth="0.7" />
          <line x1="6"  x2="14" y1="0" y2="0" stroke="#0c2754" strokeWidth="0.7" />
          <line x1="0"  x2="0"  y1="-14" y2="-6" stroke="#0c2754" strokeWidth="0.7" />
          <line x1="0"  x2="0"  y1="6"   y2="14" stroke="#0c2754" strokeWidth="0.7" />
        </g>

        {/* nodes */}
        {nodes.map((n) => {
          const x = n.x - NW / 2, y = n.y - NH / 2;
          return (
            <g key={n.peer} transform={`translate(${x},${y})`}>
              <rect x="0" y="0" width={NW} height={NH} fill="#ffffff" stroke="#0c2754" strokeWidth="1" />
              {/* engineering corner brackets just outside the box */}
              <path d={`M${-B},${B} L${-B},${-B} L${B},${-B}`} stroke="#0c2754" strokeWidth="1" fill="none" />
              <path d={`M${NW - B},${-B} L${NW + B},${-B} L${NW + B},${B}`} stroke="#0c2754" strokeWidth="1" fill="none" />
              <path d={`M${NW + B},${NH - B} L${NW + B},${NH + B} L${NW - B},${NH + B}`} stroke="#0c2754" strokeWidth="1" fill="none" />
              <path d={`M${B},${NH + B} L${-B},${NH + B} L${-B},${NH - B}`} stroke="#0c2754" strokeWidth="1" fill="none" />
              {/* labels */}
              {big ? (
                <text x={NW / 2} y={NH / 2 + 3} textAnchor="middle"
                      fontFamily="'Bricolage Grotesque', sans-serif" fontSize="9" fontWeight={700} letterSpacing="1.2" fill="#0c2754">
                  N·{n.num}
                </text>
              ) : (
                <>
                  <text x="10" y="15" fontFamily="'Bricolage Grotesque', sans-serif" fontSize="10" fontWeight={700} letterSpacing="1.4" fill="#0c2754">N·{n.num}</text>
                  <text x="10" y="28" fontFamily="'JetBrains Mono', monospace" fontSize="10" fill="#5a6b85">{n.peer.split(':')[0]}</text>
                </>
              )}
              {/* status pip — only on the larger boxes */}
              {!big && (
                <circle cx={NW - 10} cy={10} r="2.5" fill="#059669">
                  <animate attributeName="opacity" values="1;0.3;1" dur="2.2s" repeatCount="indefinite" />
                </circle>
              )}
            </g>
          );
        })}

        {nodes.length === 0 && (
          <text x={cx} y={cy} textAnchor="middle" className="schem-empty">
            no peers reporting · waiting for primer registration
          </text>
        )}
      </svg>
    </>
  );
}

// ─── Node savings heatmap ────────────────────────────────────────────
// A row of N small cells (one per node). Intensity is a deterministic
// hash of the node index modulated by current saved-memory — gives the
// "cluster pulse" visual without needing per-node telemetry plumbed up.
function NodeHeatmap({ nodeCount, savings, jvms }: { nodeCount: number; savings: Savings | null; jvms: number }) {
  const rss = savings?.totalRssKiB || 0;
  const perNodeAvg = rss / Math.max(1, nodeCount);
  const jvmsPerNode = Math.max(1, Math.round(jvms / Math.max(1, nodeCount)));

  const cols = nodeCount > 40 ? 30 : nodeCount > 20 ? 20 : nodeCount;

  return (
    <div className="heatmap-section">
      <div className="heatmap-head">
        <span className="heatmap-label">NODE SAVINGS · {nodeCount} nodes</span>
        <span className="heatmap-meta">
          {jvmsPerNode} JVMs/node · avg saved {(() => { const r = fmtKiB(perNodeAvg - perNodeAvg / 16.7); return `${r.v} ${r.unit}`; })()} per node
        </span>
      </div>
      <div className="heatmap-grid" style={{ gridTemplateColumns: `repeat(${cols}, 1fr)` }}>
        {Array.from({ length: nodeCount }, (_, i) => {
          const h = ((i * 2654435761) % 1000) / 1000;          // stable per-node hash [0..1]
          const intensity = 0.45 + h * 0.55;                    // 0.45..1.0
          const r = fmtKiB(perNodeAvg * intensity);
          return (
            <div
              key={i}
              className="hm-cell"
              style={{ opacity: intensity }}
              title={`mc-1-worker-${String(i + 1).padStart(2, '0')} · saved ${r.v} ${r.unit}`}
            />
          );
        })}
      </div>
    </div>
  );
}

// ─── Sidebar nav item ───────────────────────────────────────────────
function RailItem({ n, label, active, onClick }: { n: string; label: string; active: boolean; onClick: () => void }) {
  return (
    <button className={'rail-item ' + (active ? 'active' : '')} onClick={onClick}>
      <span className="num">{n}</span>
      <span className="label">{label}</span>
    </button>
  );
}

// ─── Per-view page header content ──────────────────────────────────
const VIEW_META: Record<View, { num: string; title: string; sub: string }> = {
  classcaches: { num: '01', title: 'ClassCaches',     sub: 'JVM CDS archives requested by your workloads, watched across every namespace. Phase reflects the most recent transition reported by the operator.' },
  archives:    { num: '02', title: 'Archives',        sub: 'Built archives advertised in Valkey, keyed by sha256(app · agent · jvm · arch · profile). Each carries the set of peers that currently host the file.' },
  topology:    { num: '03', title: 'Cluster topology',sub: 'Live peer ring. Each edge is a pair of peers that share at least one archive; the flowing markers are P2P traffic that the operator can use during pull.' },
  pods:        { num: '04', title: 'Pods',            sub: 'CPU and memory per workload pod — sourced from `kubectl top` and therefore requires metrics-server in the cluster.' },
  events:      { num: '05', title: 'Events',          sub: 'Diff of phase transitions, archive arrivals, and peer-set changes. Emitted on each poll, latest at the top.' },
  settings:    { num: '06', title: 'Settings',        sub: 'Where this app looks for the cluster and the Valkey directory. The desktop client never talks to your workloads directly.' },
};

function App() {
  const [view, setView] = useState<View>('classcaches');
  const [valkeyHost, setValkeyHost] = useState('127.0.0.1');
  const [valkeyPort, setValkeyPort] = useState(6379);
  const [diag, setDiag] = useState<Diag | null>(null);
  const [ccs, setCcs] = useState<CC[]>([]);
  const [archives, setArchives] = useState<Archive[]>([]);
  const [savings, setSavings] = useState<Savings | null>(null);
  const [history, setHistory] = useState<Savings[]>([]);
  const [pods, setPods] = useState<Pod[]>([]);
  const [cpuHistory, setCpuHistory] = useState<number[]>([]);
  const [memHistory, setMemHistory] = useState<number[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [events, setEvents] = useState<{ t: string; msg: string }[]>([]);
  const [showTelemetry, setShowTelemetry] = useState(true);
  const [demoMode, setDemoMode] = useState(false);  // flipped on by Diagnose if cluster is unreachable
  const tickN = useRef(0);
  useTick(1000);

  const pushEvent = (msg: string) =>
    setEvents((prev) => [{ t: fmtClock(), msg }, ...prev].slice(0, 120));

  // One-shot diagnose on mount — and auto-enable demo mode if nothing's
  // reachable. We keep this effect free of any deps so the demo-mode
  // auto-toggle only fires once (not every time the user edits the host
  // field in Settings).
  useEffect(() => {
    Diagnose(valkeyHost, valkeyPort).then((d) => {
      setDiag(d);
      pushEvent(d.kubectlOK ? `kubectl ok · context=${d.kubectlContext}` : `kubectl not configured`);
      pushEvent(d.valkeyReachable ? `valkey ok · ${d.valkeyAddr}` : `valkey unreachable · ${d.valkeyAddr}`);
      if (!d.kubectlOK && !d.valkeyReachable) {
        setDemoMode(true);
        pushEvent('no cluster reachable · entering DEMO MODE (synthetic data)');
      }
    }).catch((e) => setError(String(e)));
  }, []);

  // Re-diagnose (debounced) when the user changes Valkey host/port in
  // Settings — refreshes the top-strip KCTL/VLK indicators without
  // re-triggering demo-mode auto-flip or duplicate event log lines.
  const didMount = useRef(false);
  useEffect(() => {
    if (!didMount.current) { didMount.current = true; return; }
    const id = setTimeout(() => {
      Diagnose(valkeyHost, valkeyPort).then(setDiag).catch((e) => setError(String(e)));
    }, 500);
    return () => clearTimeout(id);
  }, [valkeyHost, valkeyPort]);

  // Polling loop
  useEffect(() => {
    let alive = true;
    let prevCCs = new Map<string, string>();
    let prevPeerCount = 0;
    let prevArchives = new Set<string>();

    async function tick() {
      try {
        tickN.current++;
        let cs: CC[], ars: Archive[], sv: Savings, ps: Pod[];
        if (demoMode) {
          const d = makeDemo(tickN.current);
          cs = d.ccs; ars = d.archives; sv = d.savings; ps = d.pods;
        } else {
          [cs, ars, sv, ps] = await Promise.all([
            ListClassCaches(),
            ListArchives(valkeyHost, valkeyPort),
            SampleSavings(),
            PodStats().catch(() => [] as Pod[]),
          ]);
        }
        if (!alive) return;

        for (const c of cs) {
          const id = c.namespace + '/' + c.name;
          const before = prevCCs.get(id);
          if (before === undefined) pushEvent(`+ ClassCache ${id} (${c.profile})`);
          else if (before !== c.phase) pushEvent(`→ ${id} phase ${before || '∅'} → ${c.phase || '∅'}`);
        }
        prevCCs = new Map(cs.map((c) => [c.namespace + '/' + c.name, c.phase]));

        const peerSum = ars.reduce((a, x) => a + (x.peerEndpoints?.length || 0), 0);
        if (peerSum !== prevPeerCount) {
          pushEvent(`peer set size: ${prevPeerCount} → ${peerSum}`);
          prevPeerCount = peerSum;
        }
        const curArSet = new Set(ars.map((a) => a.key));
        for (const k of curArSet) if (!prevArchives.has(k)) pushEvent(`+ archive ${k}`);
        prevArchives = curArSet;

        setCcs(cs);
        setArchives(ars);
        setSavings(sv);
        setPods(ps);
        setHistory((prev) => [...prev.slice(-(HISTORY_LEN - 1)), sv]);
        const totalCpu = ps.reduce((a, p) => a + (p.cpuMilli || 0), 0);
        const totalMem = ps.reduce((a, p) => a + (p.memMiB || 0), 0);
        setCpuHistory((prev) => [...prev.slice(-(HISTORY_LEN - 1)), totalCpu]);
        setMemHistory((prev) => [...prev.slice(-(HISTORY_LEN - 1)), totalMem]);
        setError(null);
      } catch (e) {
        if (!alive) return;
        setError(String(e));
      }
    }
    tick();
    const id = setInterval(tick, POLL_MS);
    return () => { alive = false; clearInterval(id); };
  }, [valkeyHost, valkeyPort, demoMode]);

  // ─── derived values for hero/status ───
  const savedKiB = savings?.savedKiB || 0;
  const heroSaved = useAnimatedNumber(savedKiB);
  const heroSavedFmt = fmtKiB(Math.round(heroSaved));
  const rss = savings?.totalRssKiB || 0;
  const sc = savings?.sharedCleanKiB || 0;
  const sd = savings?.sharedDirtyKiB || 0;
  const hitRate = rss === 0 ? 0 : ((sc + sd) / rss) * 100;
  const totalArchiveBytes = archives.reduce((a, x) => a + (x.sizeBytes || 0), 0);
  const arBytes = fmtBytes(totalArchiveBytes);
  const namespaces = new Set(ccs.map((c) => c.namespace)).size;
  const nodeCount = (() => {
    const s = new Set<string>();
    for (const a of archives) for (const p of (a.peerEndpoints || [])) s.add(p);
    return s.size;
  })();
  const totalCpu = pods.reduce((a, p) => a + (p.cpuMilli || 0), 0);
  const totalMem = pods.reduce((a, p) => a + (p.memMiB || 0), 0);
  const peakSaved = Math.max(0, ...history.map((h) => h.savedKiB));
  const peakSavedFmt = fmtKiB(peakSaved);

  // Largest CPU value in the current pod list — used to scale inline bars.
  const maxPodCpu = Math.max(1, ...pods.map((p) => p.cpuMilli || 0));
  const maxPodMem = Math.max(1, ...pods.map((p) => p.memMiB || 0));

  const meta = VIEW_META[view];

  return (
    <div className="app">
      <div className="scan-line" />

      {/* ── TOP STRIP ─────────────────────────────────────── */}
      <header className="topstrip">
        <div className="brand">
          <span className="brand-mark">cluster<span className="accent">·</span>classcache</span>
          <span className="brand-tag">DEMO INSTRUMENT</span>
          <span className="brand-ver">v0.12-A</span>
          {demoMode && <span className="brand-demo">DEMO MODE · synthetic data</span>}
        </div>
        <div className="topstrip-meta">
          <span className="stat">
            <span className="dot ok" /><span className="k">UTC</span>
            <span className="v">{fmtClock()}</span>
          </span>
          <span className="stat">
            <span className={'dot ' + (diag?.kubectlOK ? 'ok' : 'bad')} />
            <span className="k">KCTL</span>
            <span className={'v ' + (diag?.kubectlOK ? 'ok' : 'bad')}>
              {diag?.kubectlOK ? diag.kubectlContext : 'n/a'}
            </span>
          </span>
          <span className="stat">
            <span className={'dot ' + (diag?.valkeyReachable ? 'ok' : 'bad')} />
            <span className="k">VLK</span>
            <span className={'v ' + (diag?.valkeyReachable ? 'ok' : 'bad')}>{valkeyHost}:{valkeyPort}</span>
          </span>
        </div>
      </header>

      {/* ── BODY ─────────────────────────────────────────── */}
      <div className="body">
        <aside className="rail">
          <div className="rail-head">SECTIONS</div>
          <RailItem n="01" label="ClassCaches" active={view === 'classcaches'} onClick={() => setView('classcaches')} />
          <RailItem n="02" label="Archives"    active={view === 'archives'}    onClick={() => setView('archives')} />
          <RailItem n="03" label="Topology"    active={view === 'topology'}    onClick={() => setView('topology')} />
          <RailItem n="04" label="Pods"        active={view === 'pods'}        onClick={() => setView('pods')} />
          <RailItem n="05" label="Events"      active={view === 'events'}      onClick={() => setView('events')} />
          <div className="rail-sep" />
          <RailItem n="06" label="Settings"    active={view === 'settings'}    onClick={() => setView('settings')} />
          <div className="rail-spacer" />
          <div className="rail-foot">
            poll <b>{POLL_MS} ms</b><br />
            samples <b>{history.length}/{HISTORY_LEN}</b><br />
            window <b>{(history.length * POLL_MS / 1000).toFixed(0)} s</b>
          </div>
        </aside>

        <main className="main">
          {/* page header */}
          <div className="page-head">
            <div className="page-num">{meta.num}</div>
            <div>
              <h1 className="page-title">{meta.title}</h1>
              <div className="page-sub">{meta.sub}</div>
            </div>
            <div className="page-actions">
              {(view === 'classcaches' || view === 'archives') && (
                <button className="page-action" onClick={() => setShowTelemetry((s) => !s)}>
                  {showTelemetry ? 'Hide telemetry' : 'Show telemetry'}
                </button>
              )}
            </div>
          </div>

          {error && <div className="error-banner">{error}</div>}

          {/* ── ClassCaches view (LIVE OVERVIEW) ────────────── */}
          {view === 'classcaches' && (
            <>
              {/* ── Hero strip · 3 cells ────────────────────── */}
              <div className="hero-row hero-3col">
                <div className="hero-cell primary">
                  <div className="hero-cap">Saved across cluster · live</div>
                  <div className="hero-value xl">
                    <span className="hero-num">{tabular(heroSavedFmt.v)}</span>
                    <span className="hero-unit">{heroSavedFmt.unit}</span>
                  </div>
                  <div className="hero-sub">Σ(Rss − Pss) over {savings?.jvms ?? 0} JVM</div>
                </div>
                <div className="hero-cell hit">
                  <div className="hero-cap">mmap hit rate</div>
                  <div className="hero-value xl">
                    <span className="hero-num">{hitRate.toFixed(1)}</span>
                    <span className="hero-unit">%</span>
                  </div>
                  <div className="hero-sub">(Shared_Clean + Shared_Dirty) / Rss</div>
                </div>
                <div className="hero-cell counts">
                  <div className="count-row">
                    <span className="count-num">{savings?.jvms ?? 0}</span>
                    <span className="count-lbl">JVMs</span>
                  </div>
                  <div className="count-row">
                    <span className="count-num">{nodeCount}</span>
                    <span className="count-lbl">Nodes</span>
                  </div>
                  <div className="count-row">
                    <span className="count-num">{archives.length}</span>
                    <span className="count-lbl">Archives · {arBytes.v} {arBytes.unit}</span>
                  </div>
                  <div className="count-row">
                    <span className="count-num">{ccs.length}</span>
                    <span className="count-lbl">ClassCaches · {namespaces} ns</span>
                  </div>
                </div>
              </div>

              {/* ── Mmap spectrum + hit gauge — always visible ── */}
              <MmapInstrument savings={savings} />

              {/* ── Memory savings chart  +  mini topology ───── */}
              {showTelemetry && (
                <div className="duo-row">
                  <div className="duo-card">
                    <div className="duo-head">
                      <span className="duo-label">Memory saved · live</span>
                      <span className="duo-value">
                        {heroSavedFmt.v} {heroSavedFmt.unit}
                        <small>peak {peakSavedFmt.v} {peakSavedFmt.unit}</small>
                      </span>
                    </div>
                    <Oscilloscope
                      label=""
                      value=""
                      data={history.map((h) => h.savedKiB)}
                      color="#2563eb"
                    />
                  </div>
                  <div className="duo-card">
                    <div className="duo-head">
                      <span className="duo-label">Cluster topology · live</span>
                      <span className="duo-value">
                        {nodeCount} nodes
                        <small>{archives.length} archives</small>
                      </span>
                    </div>
                    <div className="schem mini">
                      <SchemTopology archives={archives} savings={savings} />
                    </div>
                  </div>
                </div>
              )}

              {/* ── Node savings heatmap ───────────────────── */}
              {showTelemetry && (
                <NodeHeatmap nodeCount={nodeCount} savings={savings} jvms={savings?.jvms || 0} />
              )}

              <div className="tbl cc">
                <div className="tbl-row head">
                  <div className="tbl-cell num">#</div>
                  <div className="tbl-cell"></div>
                  <div className="tbl-cell">Name</div>
                  <div className="tbl-cell">Namespace</div>
                  <div className="tbl-cell">Workload</div>
                  <div className="tbl-cell">Profile</div>
                  <div className="tbl-cell">Archive key</div>
                  <div className="tbl-cell">Phase</div>
                </div>
                {ccs.length === 0 && <div className="tbl-empty">no ClassCache CRs yet</div>}
                {ccs.slice(0, TABLE_CAP).map((c, i) => (
                  <div className="tbl-row" key={c.namespace + '/' + c.name}>
                    <div className="tbl-cell num">{String(i + 1).padStart(3, '0')}</div>
                    <div className="tbl-cell"><span className={'s-dot dot-' + (c.phase || 'unknown')} /></div>
                    <div className="tbl-cell key">{c.name}</div>
                    <div className="tbl-cell muted">{c.namespace}</div>
                    <div className="tbl-cell">{c.workloadName}</div>
                    <div className="tbl-cell muted">{c.profile}</div>
                    <div className="tbl-cell key">{c.archiveKey || '—'}</div>
                    <div className="tbl-cell"><span className={'phase phase-' + (c.phase || 'unknown')}>{c.phase || '—'}</span></div>
                  </div>
                ))}
                {ccs.length > TABLE_CAP && (
                  <div className="tbl-overflow">+ {ccs.length - TABLE_CAP} more ClassCaches not shown</div>
                )}
              </div>
            </>
          )}

          {/* ── Archives view ──────────────────────────────── */}
          {view === 'archives' && (
            <>
              {showTelemetry && <MmapInstrument savings={savings} />}
              <div className="tbl ar">
                <div className="tbl-row head">
                  <div className="tbl-cell num">#</div>
                  <div className="tbl-cell">Key (sha256[0:16])</div>
                  <div className="tbl-cell">JVM</div>
                  <div className="tbl-cell">Arch</div>
                  <div className="tbl-cell">Size</div>
                  <div className="tbl-cell">Peers</div>
                </div>
                {archives.length === 0 && <div className="tbl-empty">no archives advertised yet</div>}
                {archives.map((a, i) => {
                  const sz = fmtBytes(a.sizeBytes);
                  return (
                    <div className="tbl-row" key={a.key}>
                      <div className="tbl-cell num">{String(i + 1).padStart(2, '0')}</div>
                      <div className="tbl-cell key">{a.key}</div>
                      <div className="tbl-cell muted">{a.jvm || '—'}</div>
                      <div className="tbl-cell muted">{a.arch || '—'}</div>
                      <div className="tbl-cell">{sz.v} {sz.unit}</div>
                      <div className="tbl-cell">{a.peerEndpoints?.length || 0}</div>
                    </div>
                  );
                })}
              </div>
            </>
          )}

          {/* ── Topology view ──────────────────────────────── */}
          {view === 'topology' && (
            <div className="schem">
              <SchemTopology archives={archives} savings={savings} />
            </div>
          )}

          {/* ── Pods view ──────────────────────────────────── */}
          {view === 'pods' && (
            <>
              <div className="osc-row">
                <Oscilloscope
                  label="Cluster CPU"
                  value={pods.length === 0 ? '—' : tabular(String(totalCpu))}
                  unit={pods.length === 0 ? undefined : 'mCores'}
                  data={cpuHistory}
                  color="#d97706"
                />
                <Oscilloscope
                  label="Cluster memory"
                  value={pods.length === 0 ? '—' : tabular(String(totalMem))}
                  unit={pods.length === 0 ? undefined : 'MiB'}
                  data={memHistory}
                  color="#0ea5e9"
                />
              </div>
              <div className="tbl pods">
              <div className="tbl-row head">
                <div className="tbl-cell num">#</div>
                <div className="tbl-cell">Pod</div>
                <div className="tbl-cell">Namespace</div>
                <div className="tbl-cell">Node</div>
                <div className="tbl-cell">CPU (mCores)</div>
                <div className="tbl-cell">Memory (MiB)</div>
              </div>
              {pods.length === 0 && <div className="tbl-empty">no workload pods · or metrics-server is not installed</div>}
              {pods.slice(0, TABLE_CAP).map((p, i) => (
                <div className="tbl-row" key={p.namespace + '/' + p.name}>
                  <div className="tbl-cell num">{String(i + 1).padStart(4, '0')}</div>
                  <div className="tbl-cell key">{p.name}</div>
                  <div className="tbl-cell muted">{p.namespace}</div>
                  <div className="tbl-cell muted">{p.node}</div>
                  <div className="tbl-cell">
                    <div className="tinybar cpu">
                      <div className="track"><div className="fill" style={{ width: `${(p.cpuMilli / maxPodCpu) * 100}%` }} /></div>
                      <div className="val">{p.cpuMilli ? `${p.cpuMilli} m` : '—'}</div>
                    </div>
                  </div>
                  <div className="tbl-cell">
                    <div className="tinybar mem">
                      <div className="track"><div className="fill" style={{ width: `${(p.memMiB / maxPodMem) * 100}%` }} /></div>
                      <div className="val">{p.memMiB ? `${p.memMiB} MiB` : '—'}</div>
                    </div>
                  </div>
                </div>
              ))}
              {pods.length > TABLE_CAP && (
                <div className="tbl-overflow">+ {pods.length - TABLE_CAP} more pods not shown</div>
              )}
              </div>
            </>
          )}

          {/* ── Events view ────────────────────────────────── */}
          {view === 'events' && (
            <div className="events">
              {events.length === 0 && <div className="tbl-empty">waiting…</div>}
              {events.map((e, i) => (
                <div key={i} className="events-row">
                  <div className="t">{e.t}</div>
                  <div>{e.msg}</div>
                </div>
              ))}
            </div>
          )}

          {/* ── Settings view ──────────────────────────────── */}
          {view === 'settings' && (
            <div className="settings-panel">
              <div className="field">
                <label>Mode</label>
                <div className="toggle-row">
                  <button
                    className={'toggle-btn ' + (!demoMode ? 'on' : '')}
                    onClick={() => setDemoMode(false)}
                  >Live (kubectl + valkey)</button>
                  <button
                    className={'toggle-btn ' + (demoMode ? 'on' : '')}
                    onClick={() => setDemoMode(true)}
                  >Demo (synthetic data)</button>
                </div>
              </div>
              <div className="field">
                <label>Valkey host</label>
                <input value={valkeyHost} onChange={(e) => setValkeyHost(e.target.value)} />
              </div>
              <div className="field">
                <label>Valkey port</label>
                <input type="number" value={valkeyPort} onChange={(e) => setValkeyPort(parseInt(e.target.value || '6379', 10))} />
              </div>
              <div className="field">
                <label>kubectl current-context</label>
                <div className="readout">{diag?.kubectlContext || '(none)'}</div>
              </div>
              <div className="field">
                <label>Reachability</label>
                <div className="readout">
                  kubectl <b style={{ color: diag?.kubectlOK ? '#059669' : '#dc2626' }}>{diag?.kubectlOK ? 'ok' : 'down'}</b>
                  &nbsp;·&nbsp;
                  valkey <b style={{ color: diag?.valkeyReachable ? '#059669' : '#dc2626' }}>{diag?.valkeyReachable ? 'ok' : 'down'}</b>
                </div>
              </div>
            </div>
          )}
        </main>
      </div>

      {/* ── STATUS BAR ────────────────────────────────────── */}
      <footer className="statusbar">
        <span className="stat"><span className="k">JVM</span><span className="v">{savings?.jvms ?? 0}</span></span>
        <span className="stat"><span className="k">Saved</span><span className="v">{heroSavedFmt.v} {heroSavedFmt.unit}</span></span>
        <span className="stat"><span className="k">Hit</span><span className="v">{hitRate.toFixed(0)} %</span></span>
        <span className="stat"><span className="k">CPU</span><span className="v">{totalCpu} m</span></span>
        <span className="stat"><span className="k">RAM</span><span className="v">{totalMem} MiB</span></span>
        <span className="stat"><span className="k">ARCH</span><span className="v">{archives.length} · {arBytes.v} {arBytes.unit}</span></span>
        <span className="spacer" />
        <span className="stat">cluster-classcache · operator + primer + Valkey AOF</span>
      </footer>
    </div>
  );
}

export default App;

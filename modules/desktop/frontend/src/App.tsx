import { useEffect, useState } from 'react';
import './App.css';
import {
  Diagnose,
  ListClassCaches,
  ListArchives,
  SampleSavingsFromKind,
} from '../wailsjs/go/main/App';
import { main } from '../wailsjs/go/models';

// Tunables — the renderer keeps its own state for these so users can
// reconfigure without restarting the app.
const POLL_MS = 2000;
const HISTORY_LEN = 60; // 60 samples × 2s = 2 minutes of trend

type CC = main.ClassCacheSummary;
type Archive = main.ArchiveSummary;
type Savings = main.SavingsSnapshot;
type Diag = main.Diag;

function fmtKiB(kib: number): string {
  if (kib < 1024) return `${kib} KB`;
  if (kib < 1024 * 1024) return `${(kib / 1024).toFixed(1)} MB`;
  return `${(kib / 1024 / 1024).toFixed(2)} GB`;
}

function fmtBytes(b: number): string {
  return fmtKiB(b / 1024);
}

function Sparkline({ data, max, width = 240, height = 40 }: { data: number[]; max?: number; width?: number; height?: number; }) {
  if (data.length === 0) return <svg width={width} height={height} />;
  const peak = max ?? Math.max(1, ...data);
  const step = width / Math.max(1, data.length - 1);
  const points = data.map((v, i) => {
    const x = i * step;
    const y = height - (v / peak) * (height - 2) - 1;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(' ');
  return (
    <svg width={width} height={height}>
      <polyline points={points} fill="none" stroke="currentColor" strokeWidth={2} />
    </svg>
  );
}

function App() {
  const [valkeyHost, setValkeyHost] = useState('127.0.0.1');
  const [valkeyPort, setValkeyPort] = useState(6379);
  const [diag, setDiag] = useState<Diag | null>(null);
  const [ccs, setCcs] = useState<CC[]>([]);
  const [archives, setArchives] = useState<Archive[]>([]);
  const [savings, setSavings] = useState<Savings | null>(null);
  const [history, setHistory] = useState<Savings[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // One-shot diagnose on mount.
  useEffect(() => {
    Diagnose(valkeyHost, valkeyPort).then(setDiag).catch((e) => setError(String(e)));
  }, []);

  // Polling loop.
  useEffect(() => {
    let alive = true;
    async function tick() {
      try {
        const [cs, ars, sv] = await Promise.all([
          ListClassCaches(),
          ListArchives(valkeyHost, valkeyPort),
          SampleSavingsFromKind(),
        ]);
        if (!alive) return;
        setCcs(cs);
        setArchives(ars);
        setSavings(sv);
        setHistory((prev) => [...prev.slice(-(HISTORY_LEN - 1)), sv]);
        setError(null);
      } catch (e) {
        if (!alive) return;
        setError(String(e));
      }
    }
    tick();
    const id = setInterval(tick, POLL_MS);
    return () => { alive = false; clearInterval(id); };
  }, [valkeyHost, valkeyPort]);

  const ccByName = new Map(ccs.map((c) => [c.name + '/' + c.namespace, c]));
  const currentCC = selected && ccByName.get(selected);
  const currentArchive = currentCC ? archives.find((a) => a.key === currentCC.archiveKey) : undefined;

  return (
    <div className="cc-shell">
      <header className="cc-header">
        <div className="cc-title">cluster-classcache</div>
        <div className="cc-badges">
          {diag?.kubectlOK
            ? <span className="badge ok">kubectl: {diag.kubectlContext}</span>
            : <span className="badge bad">kubectl: not configured</span>}
          {diag?.valkeyReachable
            ? <span className="badge ok">valkey: {diag.valkeyAddr}</span>
            : <span className="badge bad">valkey: unreachable ({diag?.valkeyAddr})</span>}
        </div>
      </header>

      <div className="cc-body">
        <aside className="cc-sidebar">
          <div className="cc-sidebar-title">ClassCaches ({ccs.length})</div>
          {ccs.length === 0
            ? <div className="cc-empty">no ClassCache CRs yet</div>
            : ccs.map((c) => {
                const id = c.name + '/' + c.namespace;
                return (
                  <button
                    key={id}
                    className={'cc-item ' + (selected === id ? 'selected' : '')}
                    onClick={() => setSelected(id)}
                  >
                    <div className="cc-item-name">{c.name}</div>
                    <div className="cc-item-meta">
                      <span>{c.namespace}</span>
                      <span className={'phase phase-' + (c.phase || 'unknown')}>{c.phase || '—'}</span>
                    </div>
                  </button>
                );
              })}
        </aside>

        <main className="cc-detail">
          {error && <div className="cc-error">{error}</div>}

          {!currentCC && (
            <div className="cc-empty-detail">
              <h2>Select a ClassCache</h2>
              <p>Pick one from the left to see archive key, peer set, and live mmap savings.</p>
            </div>
          )}

          {currentCC && (
            <>
              <h2>{currentCC.name} <span className="ns">/ {currentCC.namespace}</span></h2>
              <div className="cc-grid">
                <div className="cc-card">
                  <div className="cc-card-label">ARCHIVE KEY</div>
                  <div className="cc-card-value mono">{currentCC.archiveKey || '(none yet)'}</div>
                </div>
                <div className="cc-card">
                  <div className="cc-card-label">PROFILE</div>
                  <div className="cc-card-value">{currentCC.profile}</div>
                </div>
                <div className="cc-card">
                  <div className="cc-card-label">PHASE</div>
                  <div className="cc-card-value">{currentCC.phase || '—'}</div>
                </div>
                <div className="cc-card">
                  <div className="cc-card-label">WORKLOAD</div>
                  <div className="cc-card-value">{currentCC.workloadName}</div>
                </div>
              </div>

              {currentArchive && (
                <div className="cc-section">
                  <h3>Archive</h3>
                  <div className="cc-archive-row">
                    <div>
                      <div className="cc-card-label">SIZE</div>
                      <div className="cc-card-value">{fmtBytes(currentArchive.sizeBytes)}</div>
                    </div>
                    <div>
                      <div className="cc-card-label">JVM</div>
                      <div className="cc-card-value">{currentArchive.jvm}</div>
                    </div>
                    <div>
                      <div className="cc-card-label">PEERS</div>
                      <div className="cc-card-value">{currentArchive.peerEndpoints?.length || 0}</div>
                    </div>
                  </div>
                  {(currentArchive.peerEndpoints || []).map((p) => (
                    <div key={p} className="cc-peer">● {p}</div>
                  ))}
                </div>
              )}
            </>
          )}

          <div className="cc-section">
            <h3>Memory savings (live, last {history.length}×2 s = {history.length * 2}s)</h3>
            {savings ? (
              <>
                <div className="cc-savings-row">
                  <div>
                    <div className="cc-card-label">Σ Rss</div>
                    <div className="cc-card-value">{fmtKiB(savings.totalRssKiB)}</div>
                  </div>
                  <div>
                    <div className="cc-card-label">Σ Pss</div>
                    <div className="cc-card-value">{fmtKiB(savings.totalPssKiB)}</div>
                  </div>
                  <div>
                    <div className="cc-card-label">SAVED</div>
                    <div className="cc-card-value highlight">{fmtKiB(savings.savedKiB)}</div>
                  </div>
                  <div>
                    <div className="cc-card-label">SHARED_CLEAN</div>
                    <div className="cc-card-value">{fmtKiB(savings.sharedCleanKiB)}</div>
                  </div>
                  <div>
                    <div className="cc-card-label">JVMs</div>
                    <div className="cc-card-value">{savings.jvms}</div>
                  </div>
                </div>
                <div className="cc-sparkline-row">
                  <div>
                    <div className="cc-card-label">Saved over time</div>
                    <Sparkline data={history.map((h) => h.savedKiB)} />
                  </div>
                  <div>
                    <div className="cc-card-label">Shared_Clean</div>
                    <Sparkline data={history.map((h) => h.sharedCleanKiB)} />
                  </div>
                </div>
              </>
            ) : <div className="cc-empty">collecting…</div>}
          </div>
        </main>
      </div>

      <footer className="cc-footer">
        <input
          className="cc-input"
          value={valkeyHost}
          onChange={(e) => setValkeyHost(e.target.value)}
          placeholder="valkey host"
        />
        <input
          className="cc-input"
          type="number"
          value={valkeyPort}
          onChange={(e) => setValkeyPort(parseInt(e.target.value || '6379', 10))}
        />
        <span className="cc-hint">port-forward Valkey first if needed</span>
      </footer>
    </div>
  );
}

export default App;

'use client';
import useSWR from 'swr';
import { useRouter, useParams } from 'next/navigation';
import { useState, useEffect, useRef } from 'react';
import {
  getStack, getStackLedger, scaleStack, rollbackStack, destroyStack,
  stopStack, startStack, redeployStack, downStack, restartStack,
  getStackManifest, putStackManifest,
  getServiceInspect, getServiceLogs, listStackEvents,
  listSnapshots, createSnapshot, restoreSnapshot, cloneSnapshot, deleteSnapshot,
} from '@/lib/api';
import type { ServiceStatus, LedgerEntry, ServiceDetail, OperationEvent, SnapshotInfo } from '@/lib/api';
import { Card, CardHeader, CardBody } from '@/components/Card';
import { StatusBadge } from '@/components/StatusBadge';
import { Button } from '@/components/Button';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';
import { relativeTime, fmtBytes } from '@/lib/api';

export default function StackDetailPage() {
  const router = useRouter();
  const { id } = useParams<{ id: string }>();
  const { data: stack, error, mutate } = useSWR(
    `stack/${id}`, () => getStack(id), { refreshInterval: 5000 },
  );
  const { data: ledger } = useSWR(
    `ledger/${id}`, () => getStackLedger(id), { refreshInterval: 30_000 },
  );
  const [scaleTarget, setScaleTarget] = useState<ServiceStatus | null>(null);
  const [inspectTarget, setInspectTarget] = useState<{ service: string; tab: 'overview' | 'ports' | 'env' | 'mounts' | 'logs' } | null>(null);
  const { data: events } = useSWR(`events/${id}`, () => listStackEvents(id), { refreshInterval: 10_000 });
  const { data: snapshots, mutate: mutateSnaps } = useSWR(`snapshots/${id}`, () => listSnapshots(id), { refreshInterval: 30_000 });
  const [snapshotLabel, setSnapshotLabel] = useState('');
  const [creatingSnap, setCreatingSnap] = useState(false);
  const [cloneTarget, setCloneTarget] = useState<SnapshotInfo | null>(null);
  const [destroying, setDestroying] = useState(false);
  const [rolling, setRolling] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [starting, setStarting] = useState(false);
  const [redeploying, setRedeploying] = useState(false);
  const [downing, setDowning] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [actionErr, setActionErr] = useState('');
  const [showManifest, setShowManifest] = useState(false);

  if (error) return <ErrorMessage message={error.message} />;
  if (!stack) return <LoadingScreen />;

  const isStopped = stack.status === 'stopped' || stack.status === 'stopping' || stack.status === 'down';
  const isRunning = stack.status === 'running' || stack.status === 'deploying' || stack.status === 'verifying';

  const handleRollback = async () => {
    setRolling(true);
    setActionErr('');
    try { await rollbackStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setRolling(false); }
  };

  const handleStop = async () => {
    setStopping(true);
    setActionErr('');
    try { await stopStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setStopping(false); }
  };

  const handleStart = async () => {
    setStarting(true);
    setActionErr('');
    try { await startStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setStarting(false); }
  };

  const handleRedeploy = async () => {
    setRedeploying(true);
    setActionErr('');
    try { await redeployStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setRedeploying(false); }
  };

  const handleDestroy = async () => {
    if (!confirm(`Destroy stack "${id}"? This removes all containers and volumes.`)) return;
    setDestroying(true);
    setActionErr('');
    try { await destroyStack(id); router.push('/stacks'); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setDestroying(false); }
  };

  const handleDown = async () => {
    if (!confirm(`Take "${id}" down? Containers will be removed but volumes and stack entry are preserved.`)) return;
    setDowning(true);
    setActionErr('');
    try { await downStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setDowning(false); }
  };

  const handleRestart = async () => {
    setRestarting(true);
    setActionErr('');
    try { await restartStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setRestarting(false); }
  };

  const handleCreateSnapshot = async () => {
    setCreatingSnap(true);
    setActionErr('');
    try {
      await createSnapshot(id, snapshotLabel || undefined);
      setSnapshotLabel('');
      mutateSnaps();
    } catch (e: unknown) {
      setActionErr(e instanceof Error ? e.message : String(e));
    } finally {
      setCreatingSnap(false);
    }
  };

  const handleRestoreSnapshot = async (snapId: string) => {
    if (!confirm(`Restore snapshot ${snapId}? The stack will be stopped and volumes overwritten.`)) return;
    setActionErr('');
    try { await restoreSnapshot(id, snapId); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
  };

  const handleDeleteSnapshot = async (snapId: string) => {
    if (!confirm(`Delete snapshot ${snapId}?`)) return;
    setActionErr('');
    try { await deleteSnapshot(id, snapId); mutateSnaps(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
  };

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <div>
          <button
            onClick={() => router.push('/stacks')}
            className="text-xs text-sm-muted hover:text-sm-accent mb-2 inline-flex items-center gap-1"
          >
            ← All Stacks
          </button>
          <div className="flex items-center gap-3">
            <h1 className="text-lg font-mono font-semibold">{id}</h1>
            <StatusBadge status={stack.status} size="md" />
          </div>
          {stack.version && (
            <p className="text-xs text-sm-muted mt-1">version {stack.version}</p>
          )}
        </div>
        <div className="flex items-center gap-2 flex-wrap">
          <Button variant="ghost" size="sm" onClick={() => setShowManifest(true)}>
            Edit Manifest
          </Button>
          <Button variant="secondary" size="sm" loading={redeploying} onClick={handleRedeploy}>
            Redeploy
          </Button>
          {isRunning && (
            <Button variant="secondary" size="sm" loading={restarting} onClick={handleRestart}>
              Restart
            </Button>
          )}
          {isStopped ? (
            <Button variant="secondary" size="sm" loading={starting} onClick={handleStart}>
              Start
            </Button>
          ) : isRunning ? (
            <Button variant="secondary" size="sm" loading={stopping} onClick={handleStop}>
              Stop
            </Button>
          ) : null}
          {isRunning && (
            <Button variant="secondary" size="sm" loading={downing} onClick={handleDown}>
              Down
            </Button>
          )}
          <Button variant="secondary" size="sm" loading={rolling} onClick={handleRollback}>
            Rollback
          </Button>
          <Button variant="danger" size="sm" loading={destroying} onClick={handleDestroy}>
            Destroy
          </Button>
        </div>
      </div>

      {actionErr && <ErrorMessage message={actionErr} />}

      {/* Services */}
      <Card>
        <CardHeader title="Services" subtitle={`${stack.services?.length ?? 0} total`} />
        {!stack.services?.length ? (
          <CardBody>
            <p className="text-sm-muted text-sm">No services found.</p>
          </CardBody>
        ) : (
          <div className="divide-y divide-sm-border/50">
            {stack.services.map(svc => (
              <ServiceRow
                key={svc.name}
                svc={svc}
                onScale={() => setScaleTarget(svc)}
                onInspect={() => setInspectTarget({ service: svc.name, tab: 'overview' })}
                onLogs={() => setInspectTarget({ service: svc.name, tab: 'logs' })}
              />
            ))}
          </div>
        )}
      </Card>

      {/* Events */}
      <Card>
        <CardHeader title="Operation History" subtitle={`${events?.length ?? 0} events`} />
        {!events?.length ? (
          <CardBody>
            <p className="text-sm-muted text-sm">No events recorded yet.</p>
          </CardBody>
        ) : (
          <div className="divide-y divide-sm-border/50">
            {events.map((ev) => (
              <EventRow key={ev.id} ev={ev} />
            ))}
          </div>
        )}
      </Card>

      {/* Snapshots */}
      <Card>
        <CardHeader title="Snapshots" subtitle={`${snapshots?.length ?? 0} stored`} />
        <div className="px-5 py-3 border-b border-sm-border/50 flex items-center gap-2">
          <input
            type="text"
            placeholder="Label (optional)"
            value={snapshotLabel}
            onChange={e => setSnapshotLabel(e.target.value)}
            className="flex-1 bg-sm-bg border border-sm-border rounded-md px-3 py-1.5 text-xs text-sm-text placeholder:text-sm-muted focus:outline-none focus:border-sm-accent"
          />
          <Button variant="secondary" size="sm" loading={creatingSnap} onClick={handleCreateSnapshot}>
            Create Snapshot
          </Button>
        </div>
        {!snapshots?.length ? (
          <CardBody>
            <p className="text-sm-muted text-sm">No snapshots yet.</p>
          </CardBody>
        ) : (
          <div className="divide-y divide-sm-border/50">
            {snapshots.map(snap => (
              <SnapshotRow
                key={snap.id}
                snap={snap}
                onRestore={() => handleRestoreSnapshot(snap.id)}
                onClone={() => setCloneTarget(snap)}
                onDelete={() => handleDeleteSnapshot(snap.id)}
              />
            ))}
          </div>
        )}
      </Card>

      {/* Ledger */}
      <Card>
        <CardHeader title="Deployment History" subtitle={`${ledger?.length ?? 0} entries`} />
        {!ledger?.length ? (
          <CardBody>
            <p className="text-sm-muted text-sm">No history yet.</p>
          </CardBody>
        ) : (
          <div className="divide-y divide-sm-border/50">
            {ledger.map((entry, i) => (
              <LedgerRow key={i} entry={entry} />
            ))}
          </div>
        )}
      </Card>

      {scaleTarget && (
        <ScaleModal
          stackId={id}
          svc={scaleTarget}
          onClose={() => setScaleTarget(null)}
          onScaled={() => { mutate(); setScaleTarget(null); }}
        />
      )}

      {inspectTarget && (
        <ServiceInspectDrawer
          stackId={id}
          service={inspectTarget.service}
          initialTab={inspectTarget.tab}
          onClose={() => setInspectTarget(null)}
        />
      )}

      {showManifest && (
        <ManifestEditor
          stackId={id}
          onClose={() => setShowManifest(false)}
          onSaved={() => { mutate(); setShowManifest(false); }}
        />
      )}

      {cloneTarget && (
        <CloneSnapshotModal
          stackId={id}
          snap={cloneTarget}
          onClose={() => setCloneTarget(null)}
          onCloned={() => { setCloneTarget(null); mutateSnaps(); }}
        />
      )}
    </div>
  );
}

// ── Sub-components ──────────────────────────────────────────────────────────────

function ServiceRow({
  svc, onScale, onInspect, onLogs,
}: {
  svc: ServiceStatus;
  onScale: () => void;
  onInspect: () => void;
  onLogs: () => void;
}) {
  const pct = svc.replicas > 0 ? Math.round((svc.ready / svc.replicas) * 100) : 0;
  const healthy = svc.health === 'healthy';
  return (
    <div className="flex items-center justify-between px-5 py-3 gap-4">
      {/* Left: status dot + name + health bar */}
      <div className="flex items-center gap-3 min-w-0">
        <span className={`w-2 h-2 rounded-full flex-shrink-0 ${healthy ? 'bg-sm-green' : 'bg-sm-red'}`} />
        <div className="min-w-0">
          <span className="font-mono text-sm text-sm-text truncate block">{svc.name}</span>
          <div className="flex items-center gap-2 mt-0.5">
            <div className="w-20 h-1 rounded-full bg-sm-border overflow-hidden">
              <div
                className={`h-full rounded-full transition-all ${pct === 100 ? 'bg-sm-green' : pct > 0 ? 'bg-sm-yellow' : 'bg-sm-red'}`}
                style={{ width: `${pct}%` }}
              />
            </div>
            <span className="text-xs text-sm-muted tabular-nums">
              <span className={healthy ? 'text-sm-green' : 'text-sm-red'}>{svc.ready}</span>
              /{svc.replicas} ready
            </span>
          </div>
        </div>
      </div>

      {/* Right: action buttons */}
      <div className="flex items-center gap-1.5 flex-shrink-0">
        <button
          onClick={onLogs}
          className="flex items-center gap-1 px-2.5 py-1 text-xs rounded-md bg-sm-bg border border-sm-border text-sm-muted hover:text-sm-text hover:border-sm-accent transition-colors"
        >
          <svg className="w-3 h-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
            <path d="M2 4h12M2 7h8M2 10h10M2 13h6" strokeLinecap="round"/>
          </svg>
          Logs
        </button>
        <button
          onClick={onInspect}
          className="flex items-center gap-1 px-2.5 py-1 text-xs rounded-md bg-sm-bg border border-sm-border text-sm-muted hover:text-sm-text hover:border-sm-accent transition-colors"
        >
          <svg className="w-3 h-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
            <circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5l3 3" strokeLinecap="round"/>
          </svg>
          Inspect
        </button>
        <button
          onClick={onScale}
          className="flex items-center gap-1 px-2.5 py-1 text-xs rounded-md bg-sm-bg border border-sm-border text-sm-muted hover:text-sm-text hover:border-sm-accent transition-colors"
        >
          <svg className="w-3 h-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
            <path d="M8 2v12M3 8l5-5 5 5" strokeLinecap="round" strokeLinejoin="round"/>
          </svg>
          Scale
        </button>
      </div>
    </div>
  );
}

function EventRow({ ev }: { ev: OperationEvent }) {
  const success = ev.status === 'success';
  const opColor: Record<string, string> = {
    deploy: 'text-blue-400', redeploy: 'text-blue-400',
    start: 'text-green-400', stop: 'text-yellow-400',
    destroy: 'text-red-400', scale: 'text-purple-400',
  };
  return (
    <div className="flex items-center justify-between px-5 py-2.5 text-xs">
      <div className="flex items-center gap-3 min-w-0">
        <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${success ? 'bg-sm-green' : 'bg-sm-red'}`} />
        <span className={`font-mono uppercase font-semibold ${opColor[ev.operation] ?? 'text-sm-accent'}`}>
          {ev.operation}
        </span>
        {ev.details && <span className="text-sm-muted truncate">{ev.details}</span>}
        {ev.error && <span className="text-red-400 truncate">{ev.error}</span>}
      </div>
      <div className="flex items-center gap-4 flex-shrink-0">
        <span className="text-sm-muted tabular-nums">{ev.durationMs}ms</span>
        <span className="text-sm-muted tabular-nums">{relativeTime(ev.startedAt)}</span>
      </div>
    </div>
  );
}

function LedgerRow({ entry }: { entry: LedgerEntry }) {
  return (
    <div className="flex items-center justify-between px-5 py-3 text-xs">
      <div className="flex items-center gap-4">
        <span className="font-mono text-sm-accent">{entry.version || '—'}</span>
        {entry.gitSha && (
          <span className="font-mono text-sm-muted">{entry.gitSha.slice(0, 7)}</span>
        )}
        {entry.deployedBy && (
          <span className="text-sm-muted">{entry.deployedBy}</span>
        )}
      </div>
      <span className="text-sm-muted tabular-nums">{relativeTime(entry.deployedAt)}</span>
    </div>
  );
}

// ── Scale modal ─────────────────────────────────────────────────────────────────

function ScaleModal({
  stackId, svc, onClose, onScaled,
}: {
  stackId: string;
  svc: ServiceStatus;
  onClose: () => void;
  onScaled: () => void;
}) {
  const [replicas, setReplicas] = useState(svc.replicas);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const handleScale = async () => {
    setLoading(true);
    setErr('');
    try {
      await scaleStack(stackId, { service: svc.name, replicas });
      onScaled();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-sm shadow-2xl">
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <h2 className="text-sm font-semibold">Scale — {svc.name}</h2>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>
        <div className="p-5 space-y-4">
          {err && <ErrorMessage message={err} />}
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Replicas</label>
            <input
              type="number"
              min={0}
              max={50}
              value={replicas}
              onChange={e => setReplicas(Number(e.target.value))}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent"
            />
          </div>
        </div>
        <div className="flex justify-end gap-2 px-5 py-4 border-t border-sm-border">
          <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
          <Button variant="primary" size="sm" loading={loading} onClick={handleScale}>
            Apply
          </Button>
        </div>
      </div>
    </div>
  );
}

// ── Service inspect drawer ───────────────────────────────────────────────────────

function ServiceInspectDrawer({
  stackId, service, initialTab = 'overview', onClose,
}: { stackId: string; service: string; initialTab?: 'overview' | 'ports' | 'env' | 'mounts' | 'logs'; onClose: () => void }) {
  const [tab, setTab] = useState<'overview' | 'ports' | 'env' | 'mounts' | 'logs'>(initialTab);
  const [detail, setDetail] = useState<ServiceDetail | null>(null);
  const [logs, setLogs] = useState('');
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsTail, setLogsTail] = useState(200);
  const [streaming, setStreaming] = useState(false);
  const [streamEnded, setStreamEnded] = useState(false);
  const esRef = useRef<EventSource | null>(null);
  const [err, setErr] = useState('');

  useEffect(() => {
    getServiceInspect(stackId, service)
      .then(setDetail)
      .catch(e => setErr(String(e)));
  }, [stackId, service]);

  // Close any open SSE stream on unmount or when switching away from logs tab.
  const closeStream = () => {
    if (esRef.current) {
      esRef.current.close();
      esRef.current = null;
    }
    setStreaming(false);
  };

  const startStream = () => {
    closeStream();
    setLogs('');
    setStreamEnded(false);
    setStreaming(true);
    const es = new EventSource(`/api/stacks/${stackId}/services/${service}/logs/stream`);
    esRef.current = es;
    es.onmessage = (ev) => {
      setLogs(prev => prev + ev.data + '\n');
    };
    es.addEventListener('end', () => {
      setStreamEnded(true);
      setStreaming(false);
      es.close();
      esRef.current = null;
    });
    es.onerror = () => {
      setStreamEnded(true);
      setStreaming(false);
      es.close();
      esRef.current = null;
    };
  };

  const fetchLogs = (tail: number) => {
    closeStream();
    setLogsLoading(true);
    getServiceLogs(stackId, service, tail)
      .then(text => { setLogs(text); setLogsLoading(false); })
      .catch(e => { setLogs(String(e)); setLogsLoading(false); });
  };

  useEffect(() => {
    if (tab === 'logs') startStream();
    else closeStream();
    return closeStream;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, stackId, service]);

  const tabs = ['overview', 'ports', 'env', 'mounts', 'logs'] as const;

  return (
    <div className="fixed inset-0 z-50 flex">
      {/* dim overlay */}
      <div className="flex-1 bg-black/40" onClick={onClose} />
      <div className="w-full max-w-2xl h-full bg-sm-surface border-l border-sm-border flex flex-col shadow-2xl">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border flex-shrink-0">
          <div>
            <h2 className="text-sm font-semibold font-mono">{service}</h2>
            {detail?.platform && <p className="text-xs text-sm-muted mt-0.5">platform: {detail.platform}</p>}
          </div>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>

        {/* Tabs */}
        <div className="flex gap-1 px-5 py-2 border-b border-sm-border flex-shrink-0">
          {tabs.map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`text-xs px-2.5 py-1 rounded capitalize transition-colors ${
                tab === t ? 'bg-sm-accent text-white' : 'text-sm-muted hover:text-sm-text'
              }`}
            >
              {t}
            </button>
          ))}
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto p-5">
          {err && <p className="text-xs text-red-400 mb-3">{err}</p>}
          {!detail && !err && <p className="text-sm text-sm-muted">Loading…</p>}

          {detail && tab === 'overview' && (
            <div className="space-y-4">
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-xs">
                {detail.image && <><dt className="text-sm-muted">Image</dt><dd className="font-mono text-sm-text">{detail.image}</dd></>}
                {detail.command && <><dt className="text-sm-muted">Command</dt><dd className="font-mono text-sm-text">{detail.command}</dd></>}
                {detail.created && <><dt className="text-sm-muted">Created</dt><dd className="text-sm-text">{detail.created}</dd></>}
              </dl>
              <div>
                <p className="text-xs font-semibold text-sm-muted mb-2">Instances ({detail.instances?.length ?? 0})</p>
                {!detail.instances?.length ? (
                  <p className="text-xs text-sm-muted">No instances running.</p>
                ) : (
                  <table className="w-full text-xs">
                    <thead><tr className="text-sm-muted border-b border-sm-border">
                      <th className="text-left pb-1">ID</th>
                      <th className="text-left pb-1">Status</th>
                      <th className="text-left pb-1">Health</th>
                      <th className="text-left pb-1">Node</th>
                    </tr></thead>
                    <tbody className="divide-y divide-sm-border/40">
                      {detail.instances.map(inst => (
                        <tr key={inst.id} className="py-1">
                          <td className="py-1 pr-3 font-mono text-sm-accent">{inst.id.slice(0, 12)}</td>
                          <td className="py-1 pr-3">{inst.status}</td>
                          <td className="py-1 pr-3">
                            <span className={inst.health === 'running' || inst.health === 'healthy' ? 'text-green-400' : 'text-red-400'}>
                              {inst.health}
                            </span>
                          </td>
                          <td className="py-1 text-sm-muted">{inst.node || '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            </div>
          )}

          {detail && tab === 'ports' && (
            <div>
              {!detail.ports?.length ? (
                <p className="text-xs text-sm-muted">No port bindings.</p>
              ) : (
                <table className="w-full text-xs">
                  <thead><tr className="text-sm-muted border-b border-sm-border">
                    <th className="text-left pb-1">Host</th>
                    <th className="text-left pb-1">Container</th>
                    <th className="text-left pb-1">Protocol</th>
                  </tr></thead>
                  <tbody className="divide-y divide-sm-border/40">
                    {detail.ports.map((p, i) => (
                      <tr key={i}>
                        <td className="py-1 pr-4 font-mono">{p.hostIp ? `${p.hostIp}:` : ''}{p.hostPort || '—'}</td>
                        <td className="py-1 pr-4 font-mono text-sm-accent">{p.containerPort}</td>
                        <td className="py-1 text-sm-muted">{p.protocol}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}

          {detail && tab === 'env' && (
            <div>
              {!detail.env?.length ? (
                <p className="text-xs text-sm-muted">No environment variables.</p>
              ) : (
                <ul className="space-y-1">
                  {detail.env.map((e, i) => (
                    <li key={i} className="text-xs font-mono text-sm-text break-all">{e}</li>
                  ))}
                </ul>
              )}
            </div>
          )}

          {detail && tab === 'mounts' && (
            <div>
              {!detail.mounts?.length ? (
                <p className="text-xs text-sm-muted">No mounts.</p>
              ) : (
                <table className="w-full text-xs">
                  <thead><tr className="text-sm-muted border-b border-sm-border">
                    <th className="text-left pb-1">Type</th>
                    <th className="text-left pb-1">Source</th>
                    <th className="text-left pb-1">Destination</th>
                  </tr></thead>
                  <tbody className="divide-y divide-sm-border/40">
                    {detail.mounts.map((m, i) => (
                      <tr key={i}>
                        <td className="py-1 pr-3 text-sm-muted">{m.type}</td>
                        <td className="py-1 pr-3 font-mono text-xs truncate max-w-xs">{m.source || '—'}</td>
                        <td className="py-1 font-mono text-sm-accent">{m.destination}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}

          {tab === 'logs' && (
            <div className="space-y-3">
              <div className="flex items-center gap-2 flex-wrap">
                {/* Live stream controls */}
                <div className="flex items-center gap-1.5">
                  <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${streaming ? 'bg-sm-green animate-pulse' : 'bg-sm-muted'}`} />
                  <span className="text-xs text-sm-muted">
                    {streaming ? 'Live' : streamEnded ? 'Stream ended' : 'Stopped'}
                  </span>
                </div>
                <button
                  onClick={startStream}
                  disabled={streaming}
                  className="px-2.5 py-0.5 text-xs rounded bg-sm-bg border border-sm-border text-sm-muted hover:text-sm-text disabled:opacity-40 transition-colors"
                >
                  ▶ Stream
                </button>
                <button
                  onClick={closeStream}
                  disabled={!streaming}
                  className="px-2.5 py-0.5 text-xs rounded bg-sm-bg border border-sm-border text-sm-muted hover:text-sm-text disabled:opacity-40 transition-colors"
                >
                  ■ Stop
                </button>
                <span className="text-xs text-sm-muted">|</span>
                <span className="text-xs text-sm-muted">Snapshot:</span>
                {[100, 200, 500].map(n => (
                  <button
                    key={n}
                    onClick={() => { setLogsTail(n); fetchLogs(n); }}
                    className={`px-2 py-0.5 text-xs rounded transition-colors ${logsTail === n && !streaming ? 'bg-sm-accent text-white' : 'bg-sm-bg border border-sm-border text-sm-muted hover:text-sm-text'}`}
                  >
                    {n}
                  </button>
                ))}
              </div>
              {logsLoading ? (
                <p className="text-xs text-sm-muted animate-pulse">Loading logs…</p>
              ) : (
                <pre className="text-xs font-mono text-sm-text whitespace-pre-wrap leading-relaxed bg-sm-bg rounded-md p-3 border border-sm-border/50 overflow-x-auto max-h-[60vh] overflow-y-auto">{logs || '(no output)'}</pre>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ── Snapshot row ─────────────────────────────────────────────────────────────────

function SnapshotRow({
  snap, onRestore, onClone, onDelete,
}: { snap: SnapshotInfo; onRestore: () => void; onClone: () => void; onDelete: () => void }) {
  const statusColor: Record<string, string> = {
    ready: 'bg-sm-green', creating: 'bg-sm-yellow', failed: 'bg-sm-red', deleted: 'bg-sm-muted',
  };
  return (
    <div className="flex items-center justify-between px-5 py-3 gap-4">
      <div className="flex items-center gap-3 min-w-0">
        <span className={`w-2 h-2 rounded-full flex-shrink-0 ${statusColor[snap.status] ?? 'bg-sm-muted'}`} />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-mono text-xs text-sm-accent">{snap.id}</span>
            {snap.label && <span className="text-xs text-sm-muted truncate">{snap.label}</span>}
          </div>
          <div className="text-xs text-sm-muted mt-0.5">
            {snap.volumes?.length ?? 0} volume{snap.volumes?.length !== 1 ? 's' : ''}
            {snap.sizeBytes > 0 && ` · ${fmtBytes(snap.sizeBytes)}`}
          </div>
        </div>
      </div>
      <div className="flex items-center gap-3 flex-shrink-0">
        <span className="text-xs text-sm-muted tabular-nums">{relativeTime(snap.createdAt)}</span>
        {snap.status === 'ready' && (
          <>
            <button onClick={onRestore} className="text-xs text-sm-muted hover:text-sm-yellow transition-colors">
              Restore
            </button>
            <button onClick={onClone} className="text-xs text-sm-muted hover:text-sm-accent transition-colors">
              Clone
            </button>
          </>
        )}
        <button onClick={onDelete} className="text-xs text-sm-muted hover:text-sm-red transition-colors">
          Delete
        </button>
      </div>
    </div>
  );
}

// ── Clone snapshot modal ──────────────────────────────────────────────────────────

function CloneSnapshotModal({
  stackId, snap, onClose, onCloned,
}: { stackId: string; snap: SnapshotInfo; onClose: () => void; onCloned: () => void }) {
  const [newId, setNewId] = useState(`${stackId}-clone`);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const handle = async () => {
    if (!newId.trim()) { setErr('New stack ID is required'); return; }
    setLoading(true);
    setErr('');
    try {
      await cloneSnapshot(stackId, snap.id, newId.trim());
      onCloned();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-sm shadow-2xl">
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <div>
            <h2 className="text-sm font-semibold">Clone Snapshot</h2>
            <p className="text-xs text-sm-muted mt-0.5">Creates a new independent stack from snapshot <span className="font-mono text-sm-accent">{snap.id}</span></p>
          </div>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>
        <div className="p-5 space-y-4">
          {err && <p className="text-xs text-red-400">{err}</p>}
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">New Stack ID</label>
            <input
              type="text"
              value={newId}
              onChange={e => setNewId(e.target.value)}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text font-mono focus:outline-none focus:border-sm-accent"
            />
          </div>
        </div>
        <div className="flex justify-end gap-2 px-5 py-4 border-t border-sm-border">
          <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
          <Button variant="primary" size="sm" loading={loading} onClick={handle}>
            Clone
          </Button>
        </div>
      </div>
    </div>
  );
}

// ── Manifest editor ──────────────────────────────────────────────────────────────

function ManifestEditor({
  stackId, onClose, onSaved,
}: {
  stackId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [yaml, setYaml] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  // Load current manifest on mount
  useEffect(() => {
    getStackManifest(stackId)
      .then(text => { setYaml(text); setLoading(false); })
      .catch(e => { setErr(String(e)); setLoading(false); });
  }, [stackId]);

  const handleSave = async () => {
    setSaving(true);
    setErr('');
    try {
      await putStackManifest(stackId, yaml);
      onSaved();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-3xl shadow-2xl flex flex-col" style={{ maxHeight: '90vh' }}>
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border flex-shrink-0">
          <div>
            <h2 className="text-sm font-semibold">Edit Manifest</h2>
            <p className="text-xs text-sm-muted mt-0.5">
              Saving immediately triggers a redeploy with the updated manifest.
            </p>
          </div>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>

        <div className="flex-1 overflow-hidden p-4">
          {loading ? (
            <p className="text-sm text-sm-muted text-center py-8">Loading manifest…</p>
          ) : (
            <textarea
              value={yaml}
              onChange={e => setYaml(e.target.value)}
              spellCheck={false}
              className="w-full h-full min-h-[400px] bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-xs font-mono text-sm-text focus:outline-none focus:border-sm-accent resize-none"
            />
          )}
        </div>

        {err && (
          <div className="px-4 pb-2">
            <p className="text-xs text-red-400 font-mono whitespace-pre-wrap">{err}</p>
          </div>
        )}

        <div className="flex justify-end gap-2 px-5 py-4 border-t border-sm-border flex-shrink-0">
          <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
          <Button variant="primary" size="sm" loading={saving} onClick={handleSave}>
            Save &amp; Redeploy
          </Button>
        </div>
      </div>
    </div>
  );
}

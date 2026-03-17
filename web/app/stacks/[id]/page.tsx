'use client';
import useSWR from 'swr';
import { useRouter, useParams } from 'next/navigation';
import { useState, useEffect } from 'react';
import {
  getStack, getStackLedger, scaleStack, rollbackStack, destroyStack,
  getStackManifest, putStackManifest,
} from '@/lib/api';
import type { ServiceStatus, LedgerEntry } from '@/lib/api';
import { Card, CardHeader, CardBody } from '@/components/Card';
import { StatusBadge } from '@/components/StatusBadge';
import { Button } from '@/components/Button';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';
import { relativeTime } from '@/lib/api';

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
  const [destroying, setDestroying] = useState(false);
  const [rolling, setRolling] = useState(false);
  const [actionErr, setActionErr] = useState('');
  const [showManifest, setShowManifest] = useState(false);

  if (error) return <ErrorMessage message={error.message} />;
  if (!stack) return <LoadingScreen />;

  const handleRollback = async () => {
    setRolling(true);
    setActionErr('');
    try { await rollbackStack(id); mutate(); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setRolling(false); }
  };

  const handleDestroy = async () => {
    if (!confirm(`Destroy stack "${id}"? This cannot be undone.`)) return;
    setDestroying(true);
    setActionErr('');
    try { await destroyStack(id); router.push('/stacks'); }
    catch (e: unknown) { setActionErr(e instanceof Error ? e.message : String(e)); }
    finally { setDestroying(false); }
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
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => setShowManifest(true)}>
            Edit Manifest
          </Button>
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

      {showManifest && (
        <ManifestEditor
          stackId={id}
          onClose={() => setShowManifest(false)}
          onSaved={() => { mutate(); setShowManifest(false); }}
        />
      )}
    </div>
  );
}

// ── Sub-components ──────────────────────────────────────────────────────────────

function ServiceRow({ svc, onScale }: { svc: ServiceStatus; onScale: () => void }) {
  const pct = svc.replicas > 0 ? Math.round((svc.ready / svc.replicas) * 100) : 0;
  const healthy = svc.health === 'healthy';
  return (
    <div className="flex items-center justify-between px-5 py-3 gap-4">
      <div className="flex items-center gap-3 min-w-0">
        <span
          className={`w-2 h-2 rounded-full flex-shrink-0 ${healthy ? 'bg-sm-green' : 'bg-sm-red'}`}
        />
        <span className="font-mono text-sm text-sm-text truncate">{svc.name}</span>
      </div>
      <div className="flex items-center gap-6">
        <div className="text-xs text-sm-muted tabular-nums">
          <span className={healthy ? 'text-sm-green' : 'text-sm-red'}>{svc.ready}</span>
          <span className="mx-0.5">/</span>
          <span>{svc.replicas}</span>
          <span className="ml-1">replicas</span>
        </div>
        <div className="w-16 h-1 rounded-full bg-sm-border overflow-hidden">
          <div
            className={`h-full rounded-full ${pct === 100 ? 'bg-sm-green' : pct > 0 ? 'bg-sm-yellow' : 'bg-sm-red'}`}
            style={{ width: `${pct}%` }}
          />
        </div>
        <button
          onClick={onScale}
          className="text-xs text-sm-muted hover:text-sm-accent transition-colors"
        >
          Scale
        </button>
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
              Saving will set the stack back to <span className="text-yellow-400">pending</span> and trigger a redeploy.
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

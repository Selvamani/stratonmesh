'use client';
import useSWR from 'swr';
import { useRouter } from 'next/navigation';
import { useState } from 'react';
import { listStacks, deployStack, stopStack, startStack, destroyStack, downStack, restartStack } from '@/lib/api';
import type { StackSummary } from '@/lib/api';
import { Card, CardHeader } from '@/components/Card';
import { StatusBadge } from '@/components/StatusBadge';
import { Table } from '@/components/Table';
import type { Column } from '@/components/Table';
import { Button } from '@/components/Button';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';

export default function StacksPage() {
  const router = useRouter();
  const { data: stacks, error, mutate } = useSWR('stacks', listStacks, { refreshInterval: 5000 });
  const [showDeploy, setShowDeploy] = useState(false);

  if (error) return <ErrorMessage message={error.message} />;
  if (!stacks) return <LoadingScreen />;

  const isStopped = (s: StackSummary) =>
    s.status === 'stopped' || s.status === 'stopping' || s.status === 'down';
  const isRunning = (s: StackSummary) =>
    s.status === 'running' || s.status === 'deploying' || s.status === 'verifying';

  const handleStop = async (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    await stopStack(id);
    mutate();
  };
  const handleStart = async (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    await startStack(id);
    mutate();
  };
  const handleDestroy = async (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    if (!confirm(`Destroy "${id}"? This removes all containers and volumes.`)) return;
    await destroyStack(id);
    mutate();
  };
  const handleDown = async (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    if (!confirm(`Take "${id}" down? Containers removed, volumes and stack entry kept.`)) return;
    await downStack(id);
    mutate();
  };
  const handleRestart = async (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    await restartStack(id);
    mutate();
  };

  const columns: Column<StackSummary>[] = [
    {
      key: 'id',
      header: 'Stack',
      render: (row: StackSummary) => (
        <span className="font-mono text-sm-accent">{row.id}</span>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      render: (row: StackSummary) => <StatusBadge status={row.status} size="sm" />,
    },
    {
      key: 'actions',
      header: '',
      align: 'right' as const,
      render: (row: StackSummary) => (
        <div className="flex items-center justify-end gap-1.5" onClick={e => e.stopPropagation()}>
          {isStopped(row) ? (
            <button
              onClick={e => handleStart(e, row.id)}
              className="px-2 py-0.5 text-xs rounded bg-green-900/40 text-green-400 hover:bg-green-800/60 transition-colors"
            >
              Start
            </button>
          ) : isRunning(row) ? (
            <>
              <button
                onClick={e => handleRestart(e, row.id)}
                className="px-2 py-0.5 text-xs rounded bg-blue-900/40 text-blue-400 hover:bg-blue-800/60 transition-colors"
              >
                Restart
              </button>
              <button
                onClick={e => handleStop(e, row.id)}
                className="px-2 py-0.5 text-xs rounded bg-yellow-900/40 text-yellow-400 hover:bg-yellow-800/60 transition-colors"
              >
                Stop
              </button>
              <button
                onClick={e => handleDown(e, row.id)}
                className="px-2 py-0.5 text-xs rounded bg-orange-900/40 text-orange-400 hover:bg-orange-800/60 transition-colors"
              >
                Down
              </button>
            </>
          ) : null}
          <button
            onClick={e => handleDestroy(e, row.id)}
            className="px-2 py-0.5 text-xs rounded bg-red-900/40 text-red-400 hover:bg-red-800/60 transition-colors"
          >
            Destroy
          </button>
          <span className="text-xs text-sm-muted hover:text-sm-accent transition-colors cursor-pointer">
            View →
          </span>
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-sm-muted">{stacks.length} stack{stacks.length !== 1 ? 's' : ''} deployed</p>
        <Button variant="primary" size="sm" onClick={() => setShowDeploy(true)}>
          + Deploy
        </Button>
      </div>

      <Card>
        <CardHeader title="All Stacks" />
        <Table
          columns={columns}
          rows={stacks}
          onRowClick={(row) => router.push(`/stacks/${row.id}`)}
          emptyMessage="No stacks deployed. Click '+ Deploy' to get started."
          keyField="id"
        />
      </Card>

      {showDeploy && (
        <DeployModal onClose={() => setShowDeploy(false)} onDeployed={() => { mutate(); setShowDeploy(false); }} />
      )}
    </div>
  );
}

// ── Deploy modal ───────────────────────────────────────────────────────────────

function DeployModal({ onClose, onDeployed }: { onClose: () => void; onDeployed: () => void }) {
  const [manifest, setManifest] = useState(EXAMPLE_MANIFEST);
  const [platform, setPlatform] = useState('docker');
  const [tab, setTab] = useState<'edit' | 'preview'>('edit');
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const handleDeploy = async () => {
    setLoading(true);
    setErr('');
    try {
      let yaml = manifest;
      if (platform) {
        if (/^platform:/m.test(yaml)) {
          yaml = yaml.replace(/^platform:.*$/m, `platform: ${platform}`);
        } else {
          yaml = `platform: ${platform}\n` + yaml;
        }
      }
      await deployStack(yaml);
      onDeployed();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-2xl shadow-2xl flex flex-col max-h-[90vh]">
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <h2 className="text-sm font-semibold">Deploy Stack</h2>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>

        <div className="p-5 space-y-4 overflow-y-auto flex-1">
          {err && <ErrorMessage message={err} />}

          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Platform</label>
            <select
              value={platform}
              onChange={e => setPlatform(e.target.value)}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent"
            >
              {['docker', 'compose', 'swarm', 'kubernetes', 'terraform', 'pulumi', 'process', 'nomad', 'mesos'].map(p => (
                <option key={p} value={p}>{p}</option>
              ))}
            </select>
          </div>

          <div>
            <div className="flex items-center justify-between mb-1.5">
              <label className="text-xs text-sm-muted">Manifest (YAML)</label>
              <div className="flex gap-1">
                <button
                  onClick={() => setTab('edit')}
                  className={`text-xs px-2 py-0.5 rounded transition-colors ${tab === 'edit' ? 'bg-sm-accent text-white' : 'text-sm-muted hover:text-sm-text'}`}
                >
                  Edit
                </button>
                <button
                  onClick={() => setTab('preview')}
                  className={`text-xs px-2 py-0.5 rounded transition-colors ${tab === 'preview' ? 'bg-sm-accent text-white' : 'text-sm-muted hover:text-sm-text'}`}
                >
                  Preview
                </button>
              </div>
            </div>

            {tab === 'edit' ? (
              <textarea
                value={manifest}
                onChange={e => setManifest(e.target.value)}
                rows={18}
                className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-xs font-mono text-sm-text focus:outline-none focus:border-sm-accent resize-none"
                spellCheck={false}
                placeholder="Paste or type your stack.yaml here…"
              />
            ) : (
              <ManifestPreviewPanel yaml={manifest} platform={platform} />
            )}
          </div>
        </div>

        <div className="flex justify-end gap-2 px-5 py-4 border-t border-sm-border">
          <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
          <Button variant="primary" size="sm" loading={loading} onClick={handleDeploy}>
            Deploy
          </Button>
        </div>
      </div>
    </div>
  );
}

function ManifestPreviewPanel({ yaml, platform }: { yaml: string; platform: string }) {
  const lines = yaml.split('\n');
  const withPlatform = /^platform:/m.test(yaml)
    ? yaml.replace(/^platform:.*$/m, `platform: ${platform}`)
    : `platform: ${platform}\n` + yaml;

  return (
    <div className="bg-sm-bg border border-sm-border rounded-md p-3 overflow-auto max-h-80">
      <pre className="text-xs font-mono text-sm-text whitespace-pre-wrap">{withPlatform}</pre>
      <p className="text-xs text-sm-muted mt-2">{lines.length} lines · switch to Edit tab to make changes</p>
    </div>
  );
}


const EXAMPLE_MANIFEST = `name: myapp
version: "1.0"
environment: development
services:
  - name: web
    image: nginx:alpine
    replicas: 1
    ports:
      - expose: 80
        host: 0
    resources:
      cpu: "250m"
      memory: "128Mi"
`;

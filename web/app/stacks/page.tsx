'use client';
import useSWR from 'swr';
import { useRouter } from 'next/navigation';
import { useState } from 'react';
import { listStacks, deployStack } from '@/lib/api';
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
        <span className="text-xs text-sm-muted hover:text-sm-accent transition-colors">
          View →
        </span>
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
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const handleDeploy = async () => {
    setLoading(true);
    setErr('');
    try {
      // Parse the YAML manifest as JSON for the REST API
      // The backend's POST /v1/stacks expects manifest.Stack JSON
      const parsed = parseSimpleYaml(manifest);
      parsed.platform = platform;
      await deployStack(JSON.stringify(parsed));
      onDeployed();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-2xl shadow-2xl">
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <h2 className="text-sm font-semibold">Deploy Stack</h2>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>
        <div className="p-5 space-y-4">
          {err && <ErrorMessage message={err} />}
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Platform</label>
            <select
              value={platform}
              onChange={e => setPlatform(e.target.value)}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent"
            >
              {['docker', 'compose', 'kubernetes', 'terraform', 'pulumi', 'process'].map(p => (
                <option key={p} value={p}>{p}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Manifest (YAML)</label>
            <textarea
              value={manifest}
              onChange={e => setManifest(e.target.value)}
              rows={16}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-xs font-mono text-sm-text focus:outline-none focus:border-sm-accent resize-none"
              spellCheck={false}
            />
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

// Very simple YAML → object parser for the manifest modal.
// Only handles the flat fields we need; full parsing happens server-side.
function parseSimpleYaml(yaml: string): Record<string, unknown> {
  const lines = yaml.split('\n');
  const obj: Record<string, unknown> = {};
  for (const line of lines) {
    const m = line.match(/^(\w+):\s*"?([^"]*)"?\s*$/);
    if (m) obj[m[1]] = m[2];
  }
  return obj;
}

const EXAMPLE_MANIFEST = `name: myapp
version: "1.0"
platform: docker
environment: development
services:
  - name: web
    image: nginx:alpine
    replicas: 1
    ports:
      - expose: 80
        host: 8080
    resources:
      cpu: "250m"
      memory: "128Mi"
`;

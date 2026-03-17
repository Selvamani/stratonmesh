'use client';
import useSWR from 'swr';
import { useState, useEffect } from 'react';
import { listBlueprints, deleteBlueprint } from '@/lib/api';
import type { Blueprint } from '@/lib/api';
import { Card, CardBody } from '@/components/Card';
import { Button } from '@/components/Button';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';
import { relativeTime } from '@/lib/api';

export default function CatalogPage() {
  const { data: blueprints, error, mutate } = useSWR('catalog', listBlueprints, { refreshInterval: 30_000 });
  const [showImport, setShowImport] = useState(false);
  const [selected, setSelected] = useState<Blueprint | null>(null);
  const [search, setSearch] = useState('');

  if (error) return <ErrorMessage message={error.message} />;
  if (!blueprints) return <LoadingScreen />;

  const filtered = blueprints.filter(bp =>
    bp.name.toLowerCase().includes(search.toLowerCase()) ||
    bp.description?.toLowerCase().includes(search.toLowerCase()) ||
    bp.category?.toLowerCase().includes(search.toLowerCase()),
  );

  return (
    <div className="space-y-4">
      {/* Toolbar */}
      <div className="flex items-center gap-3">
        <input
          type="text"
          placeholder="Search blueprints…"
          value={search}
          onChange={e => setSearch(e.target.value)}
          className="flex-1 bg-sm-surface border border-sm-border rounded-md px-3 py-1.5 text-sm text-sm-text placeholder-sm-muted focus:outline-none focus:border-sm-accent"
        />
        <Button variant="primary" size="sm" onClick={() => setShowImport(true)}>
          + Import
        </Button>
      </div>

      {/* Grid */}
      {filtered.length === 0 ? (
        <Card>
          <CardBody>
            <p className="text-center text-sm text-sm-muted py-8">
              {blueprints.length === 0
                ? 'Catalog is empty. Import a Git repo to add blueprints.'
                : 'No blueprints match your search.'}
            </p>
          </CardBody>
        </Card>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {filtered.map(bp => (
            <BlueprintCard key={bp.name} bp={bp} onSelect={() => setSelected(bp)} onDeleted={() => mutate()} />
          ))}
        </div>
      )}

      {showImport && (
        <ImportModal
          onClose={() => setShowImport(false)}
          onImported={() => { mutate(); setShowImport(false); }}
        />
      )}

      {selected && (
        <BlueprintDetailModal
          bp={selected}
          onClose={() => setSelected(null)}
          onDeployed={() => setSelected(null)}
        />
      )}
    </div>
  );
}

// ── Blueprint card ──────────────────────────────────────────────────────────────

const CODE_SERVER_URL = process.env.NEXT_PUBLIC_CODE_SERVER_URL ?? 'http://localhost:8443';

function editorUrl(bp: Blueprint): string | null {
  if ((bp.importMode !== 'repo' && bp.importMode !== 'ai') || !bp.localPath) return null;
  // localPath inside sm-controller is /var/lib/stratonmesh/repos/{name}
  // code-server mounts the same repos dir at /home/coder/repos
  const repoName = bp.localPath.split('/').pop() ?? bp.name;
  return `${CODE_SERVER_URL}/?folder=/home/coder/repos/${repoName}`;
}

function BlueprintCard({ bp, onSelect, onDeleted }: { bp: Blueprint; onSelect: () => void; onDeleted: () => void }) {
  const [deleting, setDeleting] = useState(false);
  const ideUrl = editorUrl(bp);

  const handleDelete = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!confirm(`Delete blueprint "${bp.name}"? This cannot be undone.`)) return;
    setDeleting(true);
    try {
      await deleteBlueprint(bp.name);
      onDeleted();
    } catch {
      setDeleting(false);
    }
  };

  return (
    <div className="relative group bg-sm-surface border border-sm-border rounded-lg p-4 hover:border-sm-accent/50 hover:bg-white/[0.02] transition-colors">
      <button onClick={onSelect} className="absolute inset-0 w-full h-full" aria-label={`Open ${bp.name}`} />
      <div className="flex items-start justify-between gap-2 mb-2">
        <span className={`font-semibold text-sm ${bp.name ? 'text-sm-text' : 'text-sm-muted italic'}`}>
          {bp.name || '(unnamed)'}
        </span>
        <div className="flex items-center gap-1.5 flex-shrink-0">
          <span className="text-xs font-mono text-sm-muted">{bp.version}</span>
          {ideUrl && (
            <a
              href={ideUrl}
              target="_blank"
              rel="noopener noreferrer"
              onClick={e => e.stopPropagation()}
              className="relative z-10 opacity-0 group-hover:opacity-100 text-sm-muted hover:text-blue-400 transition-opacity px-1 text-xs"
              title="Open in browser IDE"
            >
              IDE
            </a>
          )}
          <button
            onClick={handleDelete}
            disabled={deleting}
            className="relative z-10 opacity-0 group-hover:opacity-100 text-sm-muted hover:text-red-400 transition-opacity px-1"
            title="Delete blueprint"
          >
            {deleting ? '…' : '×'}
          </button>
        </div>
      </div>
      {bp.description && (
        <p className="text-xs text-sm-muted line-clamp-2 mb-3">{bp.description}</p>
      )}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          {(bp.importMode === 'repo' || bp.importMode === 'ai') && (
            <span className="px-1.5 py-0.5 text-xs rounded bg-green-500/10 text-green-400">repo</span>
          )}
          {bp.importMode === 'ai' && (
            <span className="px-1.5 py-0.5 text-xs rounded bg-purple-500/10 text-purple-400">AI</span>
          )}
          {bp.category && (
            <span className="px-1.5 py-0.5 text-xs rounded bg-sm-accent/10 text-sm-accent">{bp.category}</span>
          )}
          {bp.source && (
            <span className="text-xs text-sm-muted truncate max-w-[120px]">{bp.source}</span>
          )}
        </div>
        <span className="text-xs text-sm-muted tabular-nums">{relativeTime(bp.updatedAt)}</span>
      </div>
    </div>
  );
}

// ── Blueprint detail modal ──────────────────────────────────────────────────────

function BlueprintDetailModal({
  bp, onClose, onDeployed,
}: {
  bp: Blueprint;
  onClose: () => void;
  onDeployed: () => void;
}) {
  const paramKeys = Object.keys(bp.parameters ?? {});
  const [params, setParams] = useState<Record<string, string>>(bp.parameters ?? {});
  const [size, setSize] = useState('S');
  const [platform, setPlatform] = useState((bp.importMode === 'repo' || bp.importMode === 'ai') ? 'compose' : '');
  const [deployFile, setDeployFile] = useState('');
  const [repoFiles, setRepoFiles] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  // Load available deploy files for repo-mode blueprints
  useEffect(() => {
    if (bp.importMode !== 'repo' && bp.importMode !== 'ai') return;
    fetch(`/api/catalog/${bp.name}/files`)
      .then(r => r.json())
      .then(d => setRepoFiles(d.files ?? []))
      .catch(() => {});
  }, [bp.name, bp.importMode]);

  const handleInstantiate = async () => {
    setLoading(true);
    setErr('');
    try {
      const res = await fetch('/api/catalog/instantiate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: bp.name,
          sizeProfile: size,
          parameters: params,
          ...(platform ? { platform } : {}),
          ...(deployFile ? { deployFile } : {}),
        }),
      });
      if (!res.ok) {
        const text = await res.text().catch(() => res.statusText);
        throw new Error(`${res.status}: ${text}`);
      }
      onDeployed();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-lg shadow-2xl">
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <div>
            <h2 className="text-sm font-semibold">{bp.name}</h2>
            {bp.source && <p className="text-xs text-sm-muted mt-0.5">{bp.source}</p>}
          </div>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>

        <div className="p-5 space-y-4 max-h-[60vh] overflow-y-auto">
          {err && <ErrorMessage message={err} />}
          {bp.description && (
            <p className="text-sm text-sm-muted">{bp.description}</p>
          )}

          {/* Git info */}
          {bp.gitUrl && (
            <div className="text-xs font-mono bg-sm-bg border border-sm-border rounded p-2 space-y-0.5">
              <div><span className="text-sm-muted">repo: </span><span className="text-sm-text">{bp.gitUrl}</span></div>
              {bp.gitBranch && <div><span className="text-sm-muted">branch: </span><span className="text-sm-text">{bp.gitBranch}</span></div>}
              {bp.gitSha && <div><span className="text-sm-muted">sha: </span><span className="text-sm-text">{bp.gitSha.slice(0, 12)}</span></div>}
            </div>
          )}

          {/* Platform */}
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Platform</label>
            <select
              value={platform}
              onChange={e => setPlatform(e.target.value)}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent"
            >
              <option value="">Auto-detect</option>
              <option value="compose">Docker Compose</option>
              <option value="docker">Docker</option>
              <option value="kubernetes">Kubernetes</option>
              <option value="terraform">Terraform</option>
              <option value="pulumi">Pulumi</option>
            </select>
          </div>

          {/* Deploy file — only for repo-mode blueprints */}
          {(bp.importMode === 'repo' || bp.importMode === 'ai') && (
            <div>
              <label className="block text-xs text-sm-muted mb-1.5">Deploy File</label>
              <select
                value={deployFile}
                onChange={e => setDeployFile(e.target.value)}
                className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent font-mono"
              >
                <option value="">Auto-detect</option>
                {repoFiles.map(f => (
                  <option key={f} value={f}>{f}</option>
                ))}
              </select>
              {repoFiles.length === 0 && (
                <p className="text-xs text-sm-muted mt-1">Scanning repo…</p>
              )}
            </div>
          )}

          {/* Size profile */}
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Size Profile</label>
            <select
              value={size}
              onChange={e => setSize(e.target.value)}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent"
            >
              {['XS', 'S', 'M', 'L', 'XL'].map(s => (
                <option key={s} value={s}>{s}</option>
              ))}
            </select>
          </div>

          {/* Parameters */}
          {paramKeys.length > 0 && (
            <div className="space-y-3">
              <label className="block text-xs text-sm-muted">Parameters</label>
              {paramKeys.map(k => (
                <div key={k}>
                  <label className="block text-xs text-sm-muted mb-1">{k}</label>
                  <input
                    type="text"
                    value={params[k] ?? ''}
                    onChange={e => setParams(prev => ({ ...prev, [k]: e.target.value }))}
                    className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent font-mono"
                    placeholder={bp.parameters?.[k]}
                  />
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="flex justify-between items-center px-5 py-4 border-t border-sm-border">
          <div>
            {editorUrl(bp) && (
              <a
                href={editorUrl(bp)!}
                target="_blank"
                rel="noopener noreferrer"
                className="text-xs text-blue-400 hover:text-blue-300 transition-colors"
              >
                Open in IDE →
              </a>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
            <Button variant="primary" size="sm" loading={loading} onClick={handleInstantiate}>
              Deploy from Catalog
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ── Import modal ────────────────────────────────────────────────────────────────

function ImportModal({
  onClose, onImported,
}: {
  onClose: () => void;
  onImported: () => void;
}) {
  const [gitUrl, setGitUrl] = useState('');
  const [name, setName] = useState('');
  const [branch, setBranch] = useState('main');
  const [mode, setMode] = useState<'catalog' | 'repo' | 'ai'>('catalog');
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const handleImport = async () => {
    if (!gitUrl) { setErr('Git URL is required'); return; }
    setLoading(true);
    setErr('');
    try {
      const res = await fetch('/api/catalog/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ gitUrl, name: name || undefined, branch, mode }),
      });
      if (!res.ok) {
        const text = await res.text().catch(() => res.statusText);
        throw new Error(`${res.status}: ${text}`);
      }
      onImported();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="bg-sm-surface border border-sm-border rounded-lg w-full max-w-md shadow-2xl">
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <h2 className="text-sm font-semibold">Import from Git</h2>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>
        <div className="p-5 space-y-4">
          {err && <ErrorMessage message={err} />}

          {/* Import mode */}
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Import Mode</label>
            <div className="flex rounded-md overflow-hidden border border-sm-border text-xs">
              <button
                onClick={() => setMode('catalog')}
                className={`flex-1 px-3 py-2 text-left transition-colors ${mode === 'catalog' ? 'bg-sm-accent text-white' : 'text-sm-muted hover:text-sm-text'}`}
              >
                <div className="font-medium">Catalog</div>
                <div className="opacity-70 mt-0.5">Parse metadata only. Images must be pre-built.</div>
              </button>
              <button
                onClick={() => setMode('repo')}
                className={`flex-1 px-3 py-2 text-left border-l border-sm-border transition-colors ${mode === 'repo' ? 'bg-sm-accent text-white' : 'text-sm-muted hover:text-sm-text'}`}
              >
                <div className="font-medium">Repo</div>
                <div className="opacity-70 mt-0.5">Keep clone on disk. Build images from source on deploy.</div>
              </button>
              <button
                onClick={() => setMode('ai')}
                className={`flex-1 px-3 py-2 text-left border-l border-sm-border transition-colors ${mode === 'ai' ? 'bg-purple-600 text-white' : 'text-sm-muted hover:text-sm-text'}`}
              >
                <div className="font-medium">AI Import</div>
                <div className="opacity-70 mt-0.5">Claude analyzes the repo and generates the manifest.</div>
              </button>
            </div>
          </div>

          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Git URL <span className="text-red-400">*</span></label>
            <input
              type="text"
              value={gitUrl}
              onChange={e => setGitUrl(e.target.value)}
              placeholder="https://github.com/org/repo.git"
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text placeholder-sm-muted focus:outline-none focus:border-sm-accent font-mono"
            />
          </div>
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Name (optional)</label>
            <input
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              placeholder="Auto-detected from repo"
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text placeholder-sm-muted focus:outline-none focus:border-sm-accent"
            />
          </div>
          <div>
            <label className="block text-xs text-sm-muted mb-1.5">Branch</label>
            <input
              type="text"
              value={branch}
              onChange={e => setBranch(e.target.value)}
              className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent font-mono"
            />
          </div>
        </div>
        <div className="flex justify-end gap-2 px-5 py-4 border-t border-sm-border">
          <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
          <Button variant="primary" size="sm" loading={loading} onClick={handleImport}>
            Import
          </Button>
        </div>
      </div>
    </div>
  );
}

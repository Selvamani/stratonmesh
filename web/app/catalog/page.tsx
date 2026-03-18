'use client';
import useSWR from 'swr';
import { useState, useEffect } from 'react';
import { listBlueprints, deleteBlueprint, deployStack, getBlueprintManifest, regenerateBlueprintManifest, saveEditedBlueprintManifest } from '@/lib/api';
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
  const hasRepo = bp.importMode === 'repo' || bp.importMode === 'ai';
  const paramKeys = Object.keys(bp.parameters ?? {});

  const [tab, setTab] = useState<'configure' | 'manifest'>('configure');
  const [params, setParams] = useState<Record<string, string>>(bp.parameters ?? {});
  const [size, setSize] = useState('S');
  const [platform, setPlatform] = useState(hasRepo ? 'compose' : '');
  const [deployFile, setDeployFile] = useState('');
  const [repoFiles, setRepoFiles] = useState<string[]>([]);
  const [deploying, setDeploying] = useState(false);
  const [deployErr, setDeployErr] = useState('');

  // Manifest preview state
  const [manifestYaml, setManifestYaml] = useState('');
  const [originalYaml, setOriginalYaml] = useState('');
  const [manifestLoading, setManifestLoading] = useState(false);
  const [manifestErr, setManifestErr] = useState('');
  const [regenLoading, setRegenLoading] = useState(false);
  const [regenOk, setRegenOk] = useState(false);
  const [saveLoading, setSaveLoading] = useState(false);
  const [saveOk, setSaveOk] = useState(false);
  const [deployingManifest, setDeployingManifest] = useState(false);
  const manifestDirty = manifestYaml !== originalYaml && originalYaml !== '';

  // Load available deploy files for repo-mode blueprints
  useEffect(() => {
    if (!hasRepo) return;
    fetch(`/api/catalog/${bp.name}/files`)
      .then(r => r.json())
      .then(d => setRepoFiles(d.files ?? []))
      .catch(() => {});
  }, [bp.name, hasRepo]);

  // Auto-load preview when switching to manifest tab
  useEffect(() => {
    if (tab === 'manifest' && deployFile && !manifestYaml) {
      handlePreview();
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab]);

  const handlePreview = async () => {
    if (!deployFile) return;
    setManifestLoading(true);
    setManifestErr('');
    try {
      const data = await getBlueprintManifest(bp.name, deployFile);
      setManifestYaml(data.yaml);
      setOriginalYaml(data.yaml);
    } catch (e: unknown) {
      setManifestErr(e instanceof Error ? e.message : String(e));
    } finally {
      setManifestLoading(false);
    }
  };

  const handleRegenerate = async () => {
    if (!deployFile) return;
    setRegenLoading(true);
    setManifestErr('');
    setRegenOk(false);
    try {
      const data = await regenerateBlueprintManifest(bp.name, deployFile);
      setManifestYaml(data.yaml);
      setOriginalYaml(data.yaml);
      setRegenOk(true);
      setTimeout(() => setRegenOk(false), 3000);
    } catch (e: unknown) {
      setManifestErr(e instanceof Error ? e.message : String(e));
    } finally {
      setRegenLoading(false);
    }
  };

  const handleSaveEdits = async () => {
    setSaveLoading(true);
    setManifestErr('');
    setSaveOk(false);
    try {
      await saveEditedBlueprintManifest(bp.name, manifestYaml);
      setOriginalYaml(manifestYaml);
      setSaveOk(true);
      setTimeout(() => setSaveOk(false), 3000);
    } catch (e: unknown) {
      setManifestErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaveLoading(false);
    }
  };

  const handleDeployManifest = async () => {
    setDeployingManifest(true);
    setManifestErr('');
    try {
      let yaml = manifestYaml;
      if (platform && !/^platform:/m.test(yaml)) {
        yaml = `platform: ${platform}\n` + yaml;
      } else if (platform) {
        yaml = yaml.replace(/^platform:.*$/m, `platform: ${platform}`);
      }
      await deployStack(yaml);
      onDeployed();
    } catch (e: unknown) {
      setManifestErr(e instanceof Error ? e.message : String(e));
    } finally {
      setDeployingManifest(false);
    }
  };

  const handleInstantiate = async () => {
    setDeploying(true);
    setDeployErr('');
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
      setDeployErr(e instanceof Error ? e.message : String(e));
    } finally {
      setDeploying(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className={`bg-sm-surface border border-sm-border rounded-lg w-full shadow-2xl transition-all ${tab === 'manifest' ? 'max-w-3xl' : 'max-w-lg'}`}>

        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
          <div>
            <h2 className="text-sm font-semibold">{bp.name}</h2>
            {bp.source && <p className="text-xs text-sm-muted mt-0.5">{bp.source}</p>}
          </div>
          <button onClick={onClose} className="text-sm-muted hover:text-sm-text text-xl leading-none">×</button>
        </div>

        {/* Tabs */}
        <div className="flex border-b border-sm-border text-xs">
          <button
            onClick={() => setTab('configure')}
            className={`px-5 py-2.5 font-medium transition-colors ${tab === 'configure' ? 'text-sm-accent border-b-2 border-sm-accent -mb-px' : 'text-sm-muted hover:text-sm-text'}`}
          >
            Configure
          </button>
          <button
            onClick={() => setTab('manifest')}
            className={`px-5 py-2.5 font-medium transition-colors ${tab === 'manifest' ? 'text-sm-accent border-b-2 border-sm-accent -mb-px' : 'text-sm-muted hover:text-sm-text'}`}
          >
            Manifest Preview
          </button>
        </div>

        {/* Configure tab */}
        {tab === 'configure' && (
          <div className="p-5 space-y-4 max-h-[60vh] overflow-y-auto">
            {deployErr && <ErrorMessage message={deployErr} />}
            {bp.description && <p className="text-sm text-sm-muted">{bp.description}</p>}

            {bp.gitUrl && (
              <div className="text-xs font-mono bg-sm-bg border border-sm-border rounded p-2 space-y-0.5">
                <div><span className="text-sm-muted">repo: </span><span className="text-sm-text">{bp.gitUrl}</span></div>
                {bp.gitBranch && <div><span className="text-sm-muted">branch: </span><span className="text-sm-text">{bp.gitBranch}</span></div>}
                {bp.gitSha && <div><span className="text-sm-muted">sha: </span><span className="text-sm-text">{bp.gitSha.slice(0, 12)}</span></div>}
              </div>
            )}

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

            {hasRepo && (
              <div>
                <div className="flex items-center justify-between mb-1.5">
                  <label className="text-xs text-sm-muted">Deploy File</label>
                  {deployFile && (
                    <button
                      onClick={() => { setTab('manifest'); handlePreview(); }}
                      className="text-xs text-sm-accent hover:text-sm-accent/80 transition-colors"
                    >
                      Preview manifest →
                    </button>
                  )}
                </div>
                <select
                  value={deployFile}
                  onChange={e => { setDeployFile(e.target.value); setManifestYaml(''); }}
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
        )}

        {/* Manifest Preview tab */}
        {tab === 'manifest' && (
          <div className="p-5 space-y-3">
            {!hasRepo ? (
              <p className="text-sm text-sm-muted py-4 text-center">
                Manifest preview is only available for <strong>repo</strong> and <strong>ai</strong> mode blueprints.
              </p>
            ) : !deployFile ? (
              <p className="text-sm text-sm-muted py-4 text-center">
                Select a deploy file on the Configure tab first.
              </p>
            ) : (
              <>
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-xs font-mono text-sm-muted truncate">{deployFile || 'manifest'}</span>
                    {manifestDirty && (
                      <span className="text-xs text-amber-400 shrink-0">● unsaved</span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5 shrink-0">
                    {(regenOk || saveOk) && <span className="text-xs text-green-400">Saved ✓</span>}
                    <Button variant="ghost" size="sm" loading={manifestLoading} onClick={handlePreview}>
                      Refresh
                    </Button>
                    {deployFile && (
                      <Button variant="ghost" size="sm" loading={regenLoading} onClick={handleRegenerate}>
                        Re-parse
                      </Button>
                    )}
                    {manifestDirty && (
                      <Button variant="secondary" size="sm" loading={saveLoading} onClick={handleSaveEdits}>
                        Save edits
                      </Button>
                    )}
                  </div>
                </div>

                {manifestErr && <ErrorMessage message={manifestErr} />}

                {manifestLoading && !manifestYaml ? (
                  <div className="flex items-center justify-center py-12">
                    <span className="text-sm text-sm-muted">Parsing manifest…</span>
                  </div>
                ) : manifestYaml ? (
                  <textarea
                    value={manifestYaml}
                    onChange={e => setManifestYaml(e.target.value)}
                    rows={22}
                    spellCheck={false}
                    className="w-full bg-sm-bg border border-sm-border rounded-md p-3 text-xs font-mono text-sm-text focus:outline-none focus:border-sm-accent resize-y"
                  />
                ) : (
                  <div className="flex items-center justify-center py-12">
                    <Button variant="primary" size="sm" onClick={handlePreview}>
                      Load Preview
                    </Button>
                  </div>
                )}

                {manifestYaml && (
                  <div className="flex items-center justify-between">
                    <p className="text-xs text-sm-muted">
                      Edit YAML directly. <strong>Save edits</strong> persists to catalog. <strong>Re-parse</strong> regenerates from the source file.
                    </p>
                    <Button variant="primary" size="sm" loading={deployingManifest} onClick={handleDeployManifest}>
                      Deploy this manifest
                    </Button>
                  </div>
                )}
              </>
            )}
          </div>
        )}

        {/* Footer */}
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
            <Button variant="primary" size="sm" loading={deploying} onClick={handleInstantiate}>
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
  const [path, setPath] = useState('');
  const [reposDir, setReposDir] = useState('');
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
        body: JSON.stringify({ gitUrl, name: name || undefined, branch, path: path || undefined, reposDir: reposDir || undefined, mode }),
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
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-sm-muted mb-1.5">Branch</label>
              <input
                type="text"
                value={branch}
                onChange={e => setBranch(e.target.value)}
                className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text focus:outline-none focus:border-sm-accent font-mono"
              />
            </div>
            <div>
              <label className="block text-xs text-sm-muted mb-1.5">
                Sub-path <span className="text-sm-muted font-normal">(optional)</span>
              </label>
              <input
                type="text"
                value={path}
                onChange={e => setPath(e.target.value)}
                placeholder="e.g. deploy/"
                className="w-full bg-sm-bg border border-sm-border rounded-md px-3 py-2 text-sm text-sm-text placeholder-sm-muted focus:outline-none focus:border-sm-accent font-mono"
              />
            </div>
          </div>
          {!path && (
            <p className="text-xs text-sm-muted -mt-2">
              Leave blank to auto-scan the repo. Set a sub-path if deploy files are in a subdirectory (e.g. <code className="font-mono">docker/</code>).
            </p>
          )}
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

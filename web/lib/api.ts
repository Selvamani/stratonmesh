// API client — all calls proxy through Next.js rewrites to /v1/* on the backend.

const BASE = '/api';

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json', ...init?.headers },
    ...init,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json() as Promise<T>;
}

// ── Types ──────────────────────────────────────────────────────────────────────

export interface StackSummary {
  id: string;
  status: string;
}

export interface ServiceStatus {
  name: string;
  replicas: number;
  ready: number;
  health: string; // healthy | unhealthy | unknown
}

export interface StackDetail {
  id: string;
  status: string;
  version: string;
  services: ServiceStatus[];
}

export interface LedgerEntry {
  stackId: string;
  version: string;
  deployedBy: string;
  deployedAt: string;
  gitSha: string;
}

export interface Node {
  id: string;
  name: string;
  os: string;
  region: string;
  status: string;
  providers: string[];
  cpuTotal: number;
  cpuFree: number;
  memTotal: number;
  memFree: number;
  gpuTotal: number;
  costPerHr: number;
  labels: Record<string, string>;
  lastSeen: string;
}

export interface Blueprint {
  name: string;
  version: string;
  source: string;
  importMode: string;   // "catalog" | "repo"
  localPath?: string;   // set for repo-mode blueprints
  gitUrl: string;
  gitBranch: string;
  gitSha: string;
  category: string;
  description: string;
  parameters: Record<string, string>;
  createdAt: string;
  updatedAt: string;
}

export interface ScaleRequest {
  service: string;
  replicas: number;
}

export interface DeployRequest {
  name?: string;
  manifestYaml?: string;
  platform?: string;
  environment?: string;
  variables?: Record<string, string>;
  dryRun?: boolean;
}

// ── Stacks ─────────────────────────────────────────────────────────────────────

export async function listStacks(): Promise<StackSummary[]> {
  const data = await req<{ stacks: StackSummary[] }>('/stacks');
  return data.stacks ?? [];
}

export async function getStack(id: string): Promise<StackDetail> {
  return req<StackDetail>(`/stacks/${id}`);
}

export async function destroyStack(id: string): Promise<void> {
  await req(`/stacks/${id}`, { method: 'DELETE' });
}

export async function scaleStack(id: string, body: ScaleRequest): Promise<void> {
  await req(`/stacks/${id}/scale`, { method: 'POST', body: JSON.stringify(body) });
}

export async function rollbackStack(id: string): Promise<void> {
  await req(`/stacks/${id}/rollback`, { method: 'POST', body: '{}' });
}

export async function getStackLedger(id: string): Promise<LedgerEntry[]> {
  const data = await req<{ entries: LedgerEntry[] }>(`/stacks/${id}/ledger`);
  return data.entries ?? [];
}

export async function deployStack(manifest: string, platform = 'docker'): Promise<void> {
  // The backend accepts a manifest.Stack JSON; send as JSON-encoded YAML string
  await req('/stacks', {
    method: 'POST',
    body: manifest,
  });
}

// ── Nodes ──────────────────────────────────────────────────────────────────────

export async function listNodes(): Promise<Node[]> {
  const data = await req<{ nodes: Node[] }>('/nodes');
  return data.nodes ?? [];
}

// ── Catalog ────────────────────────────────────────────────────────────────────

export async function listBlueprints(): Promise<Blueprint[]> {
  const data = await req<{ blueprints: Blueprint[] }>('/catalog');
  return data.blueprints ?? [];
}

export async function getBlueprint(name: string): Promise<Blueprint> {
  return req<Blueprint>(`/catalog/${name}`);
}

export async function deleteBlueprint(name: string): Promise<void> {
  await req(`/catalog/${name}`, { method: 'DELETE' });
}

export async function getStackManifest(id: string): Promise<string> {
  const res = await fetch(`/api/stacks/${id}/manifest`);
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
  return res.text();
}

export async function putStackManifest(id: string, yaml: string): Promise<void> {
  const res = await fetch(`/api/stacks/${id}/manifest`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/yaml' },
    body: yaml,
  });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
}

// ── Helpers ────────────────────────────────────────────────────────────────────

export function fmtBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
}

export function fmtMillicores(mc: number): string {
  return mc >= 1000 ? `${(mc / 1000).toFixed(1)} cores` : `${mc}m`;
}

export function fmtPercent(used: number, total: number): string {
  if (total === 0) return '0%';
  return `${Math.round((1 - used / total) * 100)}%`;
}

export function usagePercent(free: number, total: number): number {
  if (total === 0) return 0;
  return Math.round((1 - free / total) * 100);
}

export function relativeTime(iso: string): string {
  if (!iso) return '—';
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

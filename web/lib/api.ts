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

export interface PortBinding {
  hostIp?: string;
  hostPort: string;
  containerPort: string;
  protocol: string;
}

export interface MountInfo {
  type: string;
  source: string;
  destination: string;
  mode?: string;
}

export interface InstanceInfo {
  id: string;
  name: string;
  status: string;
  health: string;
  node?: string;
  started?: string;
}

export interface ServiceDetail {
  name: string;
  image: string;
  platform: string;
  instances: InstanceInfo[];
  ports: PortBinding[];
  env: string[];
  mounts: MountInfo[];
  labels?: Record<string, string>;
  command?: string;
  created?: string;
}

export interface OperationEvent {
  id: string;
  stackId: string;
  operation: string;  // deploy | redeploy | start | stop | destroy | scale
  status: string;     // success | failed
  startedAt: string;
  finishedAt: string;
  durationMs: number;
  error?: string;
  details?: string;
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

export async function stopStack(id: string): Promise<void> {
  await req(`/stacks/${id}/stop`, { method: 'POST', body: '{}' });
}

export async function startStack(id: string): Promise<void> {
  await req(`/stacks/${id}/start`, { method: 'POST', body: '{}' });
}

/** Down — removes containers but keeps volumes and the stack entry in the system. */
export async function downStack(id: string): Promise<void> {
  await req(`/stacks/${id}/down`, { method: 'POST', body: '{}' });
}

/** Restart — stops then immediately starts all containers without losing data. */
export async function restartStack(id: string): Promise<void> {
  await req(`/stacks/${id}/restart`, { method: 'POST', body: '{}' });
}

export async function redeployStack(id: string): Promise<void> {
  await req(`/stacks/${id}/redeploy`, { method: 'POST', body: '{}' });
}

export async function getStackLedger(id: string): Promise<LedgerEntry[]> {
  const data = await req<{ entries: LedgerEntry[] }>(`/stacks/${id}/ledger`);
  return data.entries ?? [];
}

export async function deployStack(manifest: string, _platform?: string): Promise<void> {
  // Send as YAML — the backend accepts both YAML and JSON for POST /v1/stacks.
  const res = await fetch('/api/stacks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/yaml' },
    body: manifest,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status}: ${text}`);
  }
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

export interface ManifestPreview {
  blueprint: string;
  file: string;
  manifest: unknown;
  yaml: string;
}

/** Parse a deploy file from the blueprint's local repo and return the manifest preview. */
export async function getBlueprintManifest(name: string, file: string): Promise<ManifestPreview> {
  const encoded = encodeURIComponent(file);
  return req<ManifestPreview>(`/catalog/${name}/manifest?file=${encoded}`);
}

/** Re-parse a deploy file and save the result as the blueprint's stored manifest. */
export async function regenerateBlueprintManifest(name: string, file: string): Promise<ManifestPreview> {
  return req<ManifestPreview>(`/catalog/${name}/manifest`, {
    method: 'POST',
    body: JSON.stringify({ file }),
  });
}

/** Save user-edited YAML directly as the blueprint's stored manifest (no re-parse from disk). */
export async function saveEditedBlueprintManifest(name: string, yaml: string): Promise<ManifestPreview> {
  const res = await fetch(`/api/catalog/${name}/manifest`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/yaml' },
    body: yaml,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json() as Promise<ManifestPreview>;
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

// ── Snapshots ──────────────────────────────────────────────────────────────────

export interface VolumeSnap {
  name: string;
  objectKey: string;
  sizeBytes: number;
}

export interface SnapshotInfo {
  id: string;
  stackId: string;
  label?: string;
  volumes: VolumeSnap[];
  manifestVersion: string;
  createdAt: string;
  sizeBytes: number;
  status: string;  // creating | ready | failed | deleted
  error?: string;
  tags?: Record<string, string>;
}

export async function listSnapshots(stackId: string): Promise<SnapshotInfo[]> {
  const data = await req<{ snapshots: SnapshotInfo[] }>(`/stacks/${stackId}/snapshots`);
  return data.snapshots ?? [];
}

export async function createSnapshot(stackId: string, label?: string): Promise<SnapshotInfo> {
  return req<SnapshotInfo>(`/stacks/${stackId}/snapshots`, {
    method: 'POST',
    body: JSON.stringify({ label: label ?? '' }),
  });
}

export async function restoreSnapshot(stackId: string, snapshotId: string): Promise<void> {
  await req(`/stacks/${stackId}/snapshots/${snapshotId}/restore`, { method: 'POST', body: '{}' });
}

export async function cloneSnapshot(stackId: string, snapshotId: string, newStackId: string): Promise<SnapshotInfo> {
  return req<SnapshotInfo>(`/stacks/${stackId}/snapshots/${snapshotId}/clone`, {
    method: 'POST',
    body: JSON.stringify({ newStackId }),
  });
}

export async function deleteSnapshot(stackId: string, snapshotId: string): Promise<void> {
  await req(`/stacks/${stackId}/snapshots/${snapshotId}`, { method: 'DELETE' });
}

// ── Service inspect / logs ─────────────────────────────────────────────────────

export async function getServiceInspect(stackId: string, service: string): Promise<ServiceDetail> {
  return req<ServiceDetail>(`/stacks/${stackId}/services/${service}/inspect`);
}

export async function getServiceLogs(stackId: string, service: string, tail = 100): Promise<string> {
  const res = await fetch(`/api/stacks/${stackId}/services/${service}/logs?tail=${tail}`);
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
  return res.text();
}

// ── Events ─────────────────────────────────────────────────────────────────────

export async function listAllEvents(limit = 100): Promise<OperationEvent[]> {
  const data = await req<{ events: OperationEvent[] }>(`/events?limit=${limit}`);
  return data.events ?? [];
}

export async function listStackEvents(stackId: string, limit = 50): Promise<OperationEvent[]> {
  const data = await req<{ events: OperationEvent[] }>(`/stacks/${stackId}/events?limit=${limit}`);
  return data.events ?? [];
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

'use client';
import useSWR from 'swr';
import Link from 'next/link';
import { listStacks, listNodes, listBlueprints, fmtBytes, fmtMillicores, usagePercent } from '@/lib/api';
import type { StackSummary, Node } from '@/lib/api';
import { StatCard, Card, CardHeader, CardBody } from '@/components/Card';
import { StatusBadge } from '@/components/StatusBadge';
import { ProgressBar } from '@/components/ProgressBar';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';

const POLL = 5000;

export default function OverviewPage() {
  const { data: stacks, error: sErr } = useSWR('stacks', listStacks, { refreshInterval: POLL });
  const { data: nodes,  error: nErr } = useSWR('nodes',  listNodes,  { refreshInterval: POLL });
  const { data: bps }                 = useSWR('catalog', listBlueprints, { refreshInterval: 30_000 });

  if (sErr || nErr) return <ErrorMessage message={sErr?.message ?? nErr?.message} />;
  if (!stacks || !nodes) return <LoadingScreen />;

  // Derived counts
  const byStatus = countByStatus(stacks);
  const totalCPU = nodes.reduce((s, n) => s + n.cpuTotal, 0);
  const freeCPU  = nodes.reduce((s, n) => s + n.cpuFree,  0);
  const totalMem = nodes.reduce((s, n) => s + n.memTotal, 0);
  const freeMem  = nodes.reduce((s, n) => s + n.memFree,  0);

  return (
    <div className="space-y-6">
      {/* Stat row */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard label="Stacks"   value={stacks.length}           sub={`${byStatus.running ?? 0} running`}    color={stacks.length > 0 ? 'default' : 'default'} />
        <StatCard label="Running"  value={byStatus.running ?? 0}   sub="healthy stacks"  color="green"  />
        <StatCard label="Failed"   value={byStatus.failed ?? 0}    sub="need attention"  color={byStatus.failed ? 'red' : 'default'} />
        <StatCard label="Nodes"    value={nodes.length}            sub={`${nodes.filter(n => n.status === 'healthy').length} healthy`} color="accent" />
      </div>

      {/* Cluster resources + stacks */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* Cluster resources */}
        <Card>
          <CardHeader title="Cluster Resources" subtitle={`${nodes.length} nodes`} />
          <CardBody className="space-y-5">
            <div>
              <div className="flex justify-between text-xs text-sm-muted mb-2">
                <span>CPU</span>
                <span className="font-mono">{fmtMillicores(totalCPU - freeCPU)} / {fmtMillicores(totalCPU)}</span>
              </div>
              <ProgressBar value={usagePercent(freeCPU, totalCPU)} />
            </div>
            <div>
              <div className="flex justify-between text-xs text-sm-muted mb-2">
                <span>Memory</span>
                <span className="font-mono">{fmtBytes(totalMem - freeMem)} / {fmtBytes(totalMem)}</span>
              </div>
              <ProgressBar value={usagePercent(freeMem, totalMem)} />
            </div>
            <div className="pt-2 border-t border-sm-border">
              <p className="text-xs text-sm-muted mb-2">Node health</p>
              <div className="flex flex-wrap gap-1.5">
                {nodes.map(n => (
                  <NodeDot key={n.id} node={n} />
                ))}
              </div>
            </div>
          </CardBody>
        </Card>

        {/* Recent stacks */}
        <Card className="lg:col-span-2">
          <CardHeader
            title="Stacks"
            action={
              <Link href="/stacks" className="text-xs text-sm-accent hover:underline">View all →</Link>
            }
          />
          {stacks.length === 0 ? (
            <CardBody>
              <p className="text-sm-muted text-sm text-center py-8">No stacks deployed yet.</p>
            </CardBody>
          ) : (
            <div className="divide-y divide-sm-border/50">
              {stacks.slice(0, 8).map(s => (
                <StackRow key={s.id} stack={s} />
              ))}
            </div>
          )}
        </Card>
      </div>

      {/* Status breakdown + Catalog */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <Card>
          <CardHeader title="Stack Status Breakdown" />
          <CardBody>
            {Object.keys(byStatus).length === 0 ? (
              <p className="text-sm-muted text-sm">No stacks</p>
            ) : (
              <ul className="space-y-2">
                {Object.entries(byStatus).map(([status, count]) => (
                  <li key={status} className="flex items-center justify-between">
                    <StatusBadge status={status} size="sm" />
                    <span className="text-sm font-mono tabular-nums text-sm-muted">{count}</span>
                  </li>
                ))}
              </ul>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader
            title="Catalog"
            subtitle={`${bps?.length ?? 0} blueprints`}
            action={
              <Link href="/catalog" className="text-xs text-sm-accent hover:underline">Browse →</Link>
            }
          />
          <CardBody className="space-y-2">
            {(bps ?? []).slice(0, 6).map(bp => (
              <div key={bp.name} className="flex items-center justify-between py-1">
                <div>
                  <p className="text-sm text-sm-text">{bp.name}</p>
                  <p className="text-xs text-sm-muted">{bp.source}</p>
                </div>
                <span className="text-xs font-mono text-sm-muted">{bp.version}</span>
              </div>
            ))}
            {!bps?.length && (
              <p className="text-sm-muted text-sm text-center py-4">Catalog is empty.</p>
            )}
          </CardBody>
        </Card>
      </div>
    </div>
  );
}

// ── Sub-components ─────────────────────────────────────────────────────────────

function StackRow({ stack }: { stack: StackSummary }) {
  return (
    <Link href={`/stacks/${stack.id}`} className="flex items-center justify-between px-5 py-3 hover:bg-white/[0.03] transition-colors">
      <span className="text-sm font-mono text-sm-text">{stack.id}</span>
      <StatusBadge status={stack.status} size="sm" />
    </Link>
  );
}

function NodeDot({ node }: { node: Node }) {
  const healthy = node.status === 'healthy';
  return (
    <span
      title={`${node.name} (${node.region}) — ${node.status}`}
      className={`w-2.5 h-2.5 rounded-full inline-block ${healthy ? 'bg-sm-green' : 'bg-sm-red'}`}
    />
  );
}

function countByStatus(stacks: StackSummary[]): Record<string, number> {
  return stacks.reduce<Record<string, number>>((acc, s) => {
    const k = s.status || 'unknown';
    acc[k] = (acc[k] ?? 0) + 1;
    return acc;
  }, {});
}

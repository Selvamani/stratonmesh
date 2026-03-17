'use client';
import useSWR from 'swr';
import { listNodes, fmtBytes, fmtMillicores, usagePercent, relativeTime } from '@/lib/api';
import type { Node } from '@/lib/api';
import { Card, CardHeader, StatCard } from '@/components/Card';
import { StatusBadge } from '@/components/StatusBadge';
import { ProgressBar } from '@/components/ProgressBar';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';

export default function NodesPage() {
  const { data: nodes, error } = useSWR('nodes', listNodes, { refreshInterval: 5000 });

  if (error) return <ErrorMessage message={error.message} />;
  if (!nodes) return <LoadingScreen />;

  const healthy = nodes.filter(n => n.status === 'healthy').length;
  const totalCPU = nodes.reduce((s, n) => s + n.cpuTotal, 0);
  const freeCPU  = nodes.reduce((s, n) => s + n.cpuFree, 0);
  const totalMem = nodes.reduce((s, n) => s + n.memTotal, 0);
  const freeMem  = nodes.reduce((s, n) => s + n.memFree, 0);

  return (
    <div className="space-y-4">
      {/* Summary stats */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard label="Nodes"   value={nodes.length}  sub={`${healthy} healthy`}  color="accent" />
        <StatCard label="Healthy" value={healthy}        sub="online"                color="green" />
        <StatCard label="CPU"     value={`${usagePercent(freeCPU, totalCPU)}%`}  sub={`${fmtMillicores(totalCPU - freeCPU)} used`} />
        <StatCard label="Memory"  value={`${usagePercent(freeMem, totalMem)}%`}  sub={`${fmtBytes(totalMem - freeMem)} used`} />
      </div>

      {/* Node cards */}
      {nodes.length === 0 ? (
        <Card>
          <CardHeader title="Nodes" />
          <div className="px-5 py-10 text-center text-sm text-sm-muted">
            No nodes registered. Start the sm-agent on a machine to register it.
          </div>
        </Card>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {nodes.map(n => (
            <NodeCard key={n.id} node={n} />
          ))}
        </div>
      )}
    </div>
  );
}

function NodeCard({ node }: { node: Node }) {
  const cpuPct = usagePercent(node.cpuFree, node.cpuTotal);
  const memPct = usagePercent(node.memFree, node.memTotal);

  return (
    <Card>
      {/* Card header row */}
      <div className="flex items-start justify-between px-5 pt-4 pb-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-semibold text-sm-text">{node.name}</span>
            <StatusBadge status={node.status} size="sm" />
          </div>
          <p className="text-xs text-sm-muted mt-0.5">{node.region} · {node.os}</p>
        </div>
        {node.costPerHr > 0 && (
          <span className="text-xs font-mono text-sm-muted">${node.costPerHr.toFixed(3)}/hr</span>
        )}
      </div>

      {/* Resource bars */}
      <div className="px-5 pb-4 space-y-3">
        <ProgressBar
          value={cpuPct}
          label="CPU"
          showValue={false}
        />
        <div className="flex justify-between text-xs text-sm-muted -mt-2 mb-1">
          <span />
          <span className="font-mono">{fmtMillicores(node.cpuTotal - node.cpuFree)} / {fmtMillicores(node.cpuTotal)}</span>
        </div>
        <ProgressBar
          value={memPct}
          label="Memory"
          showValue={false}
        />
        <div className="flex justify-between text-xs text-sm-muted -mt-2">
          <span />
          <span className="font-mono">{fmtBytes(node.memTotal - node.memFree)} / {fmtBytes(node.memTotal)}</span>
        </div>
      </div>

      {/* Footer: providers + last seen */}
      <div className="flex items-center justify-between px-5 py-3 border-t border-sm-border/50">
        <div className="flex flex-wrap gap-1">
          {node.providers?.map(p => (
            <span
              key={p}
              className="px-1.5 py-0.5 text-xs rounded bg-sm-accent/10 text-sm-accent font-mono"
            >
              {p}
            </span>
          ))}
        </div>
        <span className="text-xs text-sm-muted tabular-nums">
          {relativeTime(node.lastSeen)}
        </span>
      </div>
    </Card>
  );
}

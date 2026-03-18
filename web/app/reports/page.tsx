'use client';
import useSWR from 'swr';
import { useRouter } from 'next/navigation';
import { listAllEvents, relativeTime } from '@/lib/api';
import type { OperationEvent } from '@/lib/api';
import { Card, CardHeader } from '@/components/Card';
import { LoadingScreen, ErrorMessage } from '@/components/Empty';

export default function ReportsPage() {
  const router = useRouter();
  const { data: events, error } = useSWR('events/all', () => listAllEvents(200), { refreshInterval: 10_000 });

  if (error) return <ErrorMessage message={error.message} />;
  if (!events) return <LoadingScreen />;

  const successCount = events.filter(e => e.status === 'success').length;
  const failedCount  = events.filter(e => e.status === 'failed').length;

  return (
    <div className="space-y-4">
      {/* Summary */}
      <div className="grid grid-cols-3 gap-3">
        <StatCard label="Total Operations" value={events.length} color="text-sm-accent" />
        <StatCard label="Successful" value={successCount} color="text-green-400" />
        <StatCard label="Failed" value={failedCount} color="text-red-400" />
      </div>

      {/* Event log */}
      <Card>
        <CardHeader title="Operation Log" subtitle={`${events.length} events`} />
        {!events.length ? (
          <div className="px-5 py-8 text-center text-sm text-sm-muted">
            No operations recorded yet. Deploy a stack to get started.
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-sm-border text-sm-muted">
                  <th className="text-left px-5 py-2.5">Time</th>
                  <th className="text-left px-3 py-2.5">Stack</th>
                  <th className="text-left px-3 py-2.5">Operation</th>
                  <th className="text-left px-3 py-2.5">Status</th>
                  <th className="text-left px-3 py-2.5">Duration</th>
                  <th className="text-left px-3 py-2.5">Details / Error</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-sm-border/40">
                {events.map(ev => (
                  <EventTableRow key={ev.id} ev={ev} onStackClick={() => router.push(`/stacks/${ev.stackId}`)} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

const OP_COLOR: Record<string, string> = {
  deploy:   'bg-blue-900/40 text-blue-400',
  redeploy: 'bg-blue-900/40 text-blue-400',
  start:    'bg-green-900/40 text-green-400',
  stop:     'bg-yellow-900/40 text-yellow-400',
  destroy:  'bg-red-900/40 text-red-400',
  scale:    'bg-purple-900/40 text-purple-400',
};

function EventTableRow({ ev, onStackClick }: { ev: OperationEvent; onStackClick: () => void }) {
  const success = ev.status === 'success';
  return (
    <tr className="hover:bg-white/3 transition-colors">
      <td className="px-5 py-2.5 text-sm-muted tabular-nums whitespace-nowrap">{relativeTime(ev.startedAt)}</td>
      <td className="px-3 py-2.5">
        <button
          onClick={onStackClick}
          className="font-mono text-sm-accent hover:underline"
        >
          {ev.stackId}
        </button>
      </td>
      <td className="px-3 py-2.5">
        <span className={`px-1.5 py-0.5 rounded text-xs font-semibold uppercase ${OP_COLOR[ev.operation] ?? 'bg-sm-border text-sm-muted'}`}>
          {ev.operation}
        </span>
      </td>
      <td className="px-3 py-2.5">
        <span className={success ? 'text-green-400' : 'text-red-400'}>
          {success ? 'success' : 'failed'}
        </span>
      </td>
      <td className="px-3 py-2.5 text-sm-muted tabular-nums">{ev.durationMs}ms</td>
      <td className="px-3 py-2.5 max-w-xs truncate">
        {ev.error ? (
          <span className="text-red-400">{ev.error}</span>
        ) : ev.details ? (
          <span className="text-sm-muted font-mono">{ev.details}</span>
        ) : (
          <span className="text-sm-muted">—</span>
        )}
      </td>
    </tr>
  );
}

function StatCard({ label, value, color }: { label: string; value: number; color: string }) {
  return (
    <div className="bg-sm-surface border border-sm-border rounded-lg px-4 py-3">
      <p className="text-xs text-sm-muted">{label}</p>
      <p className={`text-2xl font-semibold mt-1 ${color}`}>{value}</p>
    </div>
  );
}

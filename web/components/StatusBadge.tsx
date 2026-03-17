import clsx from 'clsx';

const STATUS_MAP: Record<string, { color: string; dot: string; label?: string }> = {
  running:      { color: 'text-sm-green  bg-sm-green/10  border-sm-green/30',  dot: 'bg-sm-green'  },
  healthy:      { color: 'text-sm-green  bg-sm-green/10  border-sm-green/30',  dot: 'bg-sm-green'  },
  deploying:    { color: 'text-sm-accent bg-sm-accent/10 border-sm-accent/30', dot: 'bg-sm-accent animate-pulse' },
  provisioning: { color: 'text-sm-accent bg-sm-accent/10 border-sm-accent/30', dot: 'bg-sm-accent animate-pulse' },
  scheduling:   { color: 'text-sm-accent bg-sm-accent/10 border-sm-accent/30', dot: 'bg-sm-accent animate-pulse' },
  verifying:    { color: 'text-sm-accent bg-sm-accent/10 border-sm-accent/30', dot: 'bg-sm-accent animate-pulse' },
  pending:      { color: 'text-sm-yellow bg-sm-yellow/10 border-sm-yellow/30', dot: 'bg-sm-yellow'  },
  draining:     { color: 'text-sm-yellow bg-sm-yellow/10 border-sm-yellow/30', dot: 'bg-sm-yellow'  },
  rolling_back: { color: 'text-sm-orange bg-sm-orange/10 border-sm-orange/30', dot: 'bg-sm-orange animate-pulse', label: 'rolling back' },
  failed:       { color: 'text-sm-red   bg-sm-red/10    border-sm-red/30',    dot: 'bg-sm-red'    },
  unhealthy:    { color: 'text-sm-red   bg-sm-red/10    border-sm-red/30',    dot: 'bg-sm-red'    },
  stopped:      { color: 'text-sm-muted bg-white/5      border-sm-border',    dot: 'bg-sm-muted'  },
  unknown:      { color: 'text-sm-muted bg-white/5      border-sm-border',    dot: 'bg-sm-muted'  },
};

interface Props {
  status: string;
  size?: 'sm' | 'md';
}

export function StatusBadge({ status, size = 'md' }: Props) {
  const key = status?.toLowerCase().replace(/[\s-]/g, '_') ?? 'unknown';
  const cfg = STATUS_MAP[key] ?? STATUS_MAP['unknown'];

  return (
    <span
      className={clsx(
        'inline-flex items-center gap-1.5 rounded-full border font-medium capitalize',
        cfg.color,
        size === 'sm' ? 'px-2 py-0.5 text-xs' : 'px-2.5 py-1 text-xs',
      )}
    >
      <span className={clsx('rounded-full flex-shrink-0', cfg.dot, size === 'sm' ? 'w-1.5 h-1.5' : 'w-1.5 h-1.5')} />
      {cfg.label ?? key.replace(/_/g, ' ')}
    </span>
  );
}

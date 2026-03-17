import clsx from 'clsx';

interface Props {
  value: number; // 0-100
  color?: 'green' | 'yellow' | 'red' | 'accent';
  label?: string;
  showValue?: boolean;
}

function colorClass(value: number, explicit?: Props['color']) {
  if (explicit) {
    return { green: 'bg-sm-green', yellow: 'bg-sm-yellow', red: 'bg-sm-red', accent: 'bg-sm-accent' }[explicit];
  }
  if (value >= 85) return 'bg-sm-red';
  if (value >= 70) return 'bg-sm-yellow';
  return 'bg-sm-green';
}

export function ProgressBar({ value, color, label, showValue = true }: Props) {
  const pct = Math.min(100, Math.max(0, value));
  return (
    <div className="w-full">
      {(label || showValue) && (
        <div className="flex items-center justify-between mb-1 text-xs text-sm-muted">
          {label && <span>{label}</span>}
          {showValue && <span className="tabular-nums">{pct}%</span>}
        </div>
      )}
      <div className="h-1.5 w-full rounded-full bg-sm-border overflow-hidden">
        <div
          className={clsx('h-full rounded-full transition-all duration-300', colorClass(pct, color))}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

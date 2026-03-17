import clsx from 'clsx';

interface CardProps {
  children: React.ReactNode;
  className?: string;
}

export function Card({ children, className }: CardProps) {
  return (
    <div className={clsx('bg-sm-surface border border-sm-border rounded-lg', className)}>
      {children}
    </div>
  );
}

interface CardHeaderProps {
  title: string;
  subtitle?: string;
  action?: React.ReactNode;
}

export function CardHeader({ title, subtitle, action }: CardHeaderProps) {
  return (
    <div className="flex items-center justify-between px-5 py-4 border-b border-sm-border">
      <div>
        <h2 className="text-sm font-semibold text-sm-text">{title}</h2>
        {subtitle && <p className="text-xs text-sm-muted mt-0.5">{subtitle}</p>}
      </div>
      {action && <div className="flex items-center gap-2">{action}</div>}
    </div>
  );
}

export function CardBody({ children, className }: CardProps) {
  return <div className={clsx('p-5', className)}>{children}</div>;
}

interface StatCardProps {
  label: string;
  value: string | number;
  sub?: string;
  color?: 'default' | 'green' | 'yellow' | 'red' | 'accent';
}

const COLOR_MAP = {
  default: 'text-sm-text',
  green:   'text-sm-green',
  yellow:  'text-sm-yellow',
  red:     'text-sm-red',
  accent:  'text-sm-accent',
};

export function StatCard({ label, value, sub, color = 'default' }: StatCardProps) {
  return (
    <Card className="p-5">
      <p className="text-xs text-sm-muted uppercase tracking-wider mb-1">{label}</p>
      <p className={clsx('text-3xl font-bold tabular-nums', COLOR_MAP[color])}>{value}</p>
      {sub && <p className="text-xs text-sm-muted mt-1">{sub}</p>}
    </Card>
  );
}

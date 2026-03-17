import clsx from 'clsx';

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
  size?: 'sm' | 'md';
  loading?: boolean;
}

const VARIANTS = {
  primary:   'bg-sm-accent text-white hover:bg-sm-accent/80 border-transparent',
  secondary: 'bg-white/5 text-sm-text hover:bg-white/10 border-sm-border',
  danger:    'bg-sm-red/10 text-sm-red hover:bg-sm-red/20 border-sm-red/30',
  ghost:     'bg-transparent text-sm-muted hover:text-sm-text hover:bg-white/5 border-transparent',
};

const SIZES = {
  sm: 'px-3 py-1.5 text-xs',
  md: 'px-4 py-2 text-sm',
};

export function Button({
  variant = 'secondary',
  size = 'md',
  loading = false,
  className,
  children,
  disabled,
  ...props
}: ButtonProps) {
  return (
    <button
      {...props}
      disabled={disabled || loading}
      className={clsx(
        'inline-flex items-center gap-2 rounded-md border font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-sm-accent/50 disabled:opacity-50 disabled:cursor-not-allowed',
        VARIANTS[variant],
        SIZES[size],
        className,
      )}
    >
      {loading && (
        <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
          <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
          <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v4l3-3-3-3V4A10 10 0 002 12h2z" />
        </svg>
      )}
      {children}
    </button>
  );
}

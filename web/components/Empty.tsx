interface Props {
  icon?: React.ReactNode;
  title: string;
  description?: string;
  action?: React.ReactNode;
}

export function Empty({ icon, title, description, action }: Props) {
  return (
    <div className="flex flex-col items-center justify-center py-20 px-6 text-center">
      {icon && <div className="text-sm-border mb-4">{icon}</div>}
      <p className="text-sm-text font-medium mb-1">{title}</p>
      {description && <p className="text-sm-muted text-sm mb-4">{description}</p>}
      {action}
    </div>
  );
}

export function Spinner({ size = 6 }: { size?: number }) {
  return (
    <svg
      className={`w-${size} h-${size} animate-spin text-sm-accent`}
      fill="none"
      viewBox="0 0 24 24"
    >
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
      <path className="opacity-75" fill="currentColor"
        d="M4 12a8 8 0 018-8v4l3-3-3-3V4A10 10 0 002 12h2z" />
    </svg>
  );
}

export function LoadingScreen() {
  return (
    <div className="flex items-center justify-center py-24">
      <Spinner size={8} />
    </div>
  );
}

export function ErrorMessage({ message }: { message: string }) {
  return (
    <div className="rounded-md bg-sm-red/10 border border-sm-red/30 text-sm-red text-sm px-4 py-3">
      {message}
    </div>
  );
}

'use client';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import clsx from 'clsx';

const NAV = [
  { href: '/',         label: 'Overview',  icon: GridIcon },
  { href: '/stacks',   label: 'Stacks',    icon: LayersIcon },
  { href: '/nodes',    label: 'Nodes',     icon: ServerIcon },
  { href: '/catalog',  label: 'Catalog',   icon: BookIcon },
  { href: '/reports',  label: 'Reports',   icon: ReportIcon },
];

export function Sidebar() {
  const path = usePathname();
  return (
    <aside className="w-56 flex-shrink-0 bg-sm-surface border-r border-sm-border flex flex-col">
      {/* Logo */}
      <div className="h-14 flex items-center px-4 border-b border-sm-border gap-2">
        <HexIcon className="text-sm-accent w-6 h-6" />
        <span className="font-semibold text-sm-text text-sm tracking-wide">StratonMesh</span>
      </div>

      {/* Nav */}
      <nav className="flex-1 py-3 px-2 space-y-0.5">
        {NAV.map(({ href, label, icon: Icon }) => {
          const active = href === '/' ? path === '/' : path.startsWith(href);
          return (
            <Link
              key={href}
              href={href}
              className={clsx(
                'flex items-center gap-2.5 px-3 py-2 rounded-md text-sm transition-colors',
                active
                  ? 'bg-sm-accent/10 text-sm-accent font-medium'
                  : 'text-sm-muted hover:text-sm-text hover:bg-white/5',
              )}
            >
              <Icon className="w-4 h-4 flex-shrink-0" />
              {label}
            </Link>
          );
        })}
      </nav>

      {/* Footer */}
      <div className="px-4 py-3 border-t border-sm-border text-xs text-sm-muted font-mono">
        v{process.env.NEXT_PUBLIC_VERSION ?? 'dev'}
      </div>
    </aside>
  );
}

// ── Inline SVG icons ────────────────────────────────────────────────────────

function HexIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
      <path strokeLinecap="round" strokeLinejoin="round"
        d="M12 2L4 6.5v11L12 22l8-4.5v-11L12 2z" />
      <path strokeLinecap="round" strokeLinejoin="round" d="M12 2v20M4 6.5l8 5.5 8-5.5" />
    </svg>
  );
}

function GridIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
      <rect x="3" y="3" width="7" height="7" rx="1" />
      <rect x="14" y="3" width="7" height="7" rx="1" />
      <rect x="3" y="14" width="7" height="7" rx="1" />
      <rect x="14" y="14" width="7" height="7" rx="1" />
    </svg>
  );
}

function LayersIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
      <path strokeLinecap="round" strokeLinejoin="round" d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" />
    </svg>
  );
}

function ServerIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
      <rect x="2" y="3" width="20" height="5" rx="1" />
      <rect x="2" y="10" width="20" height="5" rx="1" />
      <rect x="2" y="17" width="20" height="5" rx="1" />
      <circle cx="18" cy="5.5" r="1" fill="currentColor" stroke="none" />
      <circle cx="18" cy="12.5" r="1" fill="currentColor" stroke="none" />
      <circle cx="18" cy="19.5" r="1" fill="currentColor" stroke="none" />
    </svg>
  );
}

function BookIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
      <path strokeLinecap="round" strokeLinejoin="round"
        d="M4 19.5A2.5 2.5 0 016.5 17H20M4 19.5A2.5 2.5 0 004 17V4.5A2.5 2.5 0 016.5 2H20v17H6.5" />
    </svg>
  );
}

function ReportIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
      <rect x="3" y="3" width="18" height="18" rx="2" />
      <path strokeLinecap="round" d="M7 8h10M7 12h10M7 16h6" />
    </svg>
  );
}

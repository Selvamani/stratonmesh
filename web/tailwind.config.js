/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    './app/**/*.{js,ts,jsx,tsx,mdx}',
    './components/**/*.{js,ts,jsx,tsx,mdx}',
  ],
  theme: {
    extend: {
      colors: {
        // StratonMesh brand palette
        sm: {
          bg:       '#0d1117',
          surface:  '#161b22',
          border:   '#30363d',
          muted:    '#8b949e',
          text:     '#e6edf3',
          accent:   '#58a6ff',
          green:    '#3fb950',
          yellow:   '#d29922',
          red:      '#f85149',
          purple:   '#bc8cff',
          orange:   '#f0883e',
        },
      },
      fontFamily: {
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace'],
      },
      animation: {
        'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        'spin-slow':  'spin 3s linear infinite',
      },
    },
  },
  plugins: [],
};

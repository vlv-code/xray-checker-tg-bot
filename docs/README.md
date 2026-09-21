# Xray Checker Documentation

Official multi-language documentation for **Xray Checker with Telegram Bot**, built with [Astro Starlight](https://starlight.astro.build).

## 🌐 Supported Languages

The documentation is organized by locale under `src/content/docs/`:
- **English (Default)**: `src/content/docs/`
- **Русский (Russian)**: `src/content/docs/ru/`
- **فارسی (Persian / Farsi)**: `src/content/docs/fa/`

## 📁 Directory Structure

```
docs/
├── public/                # Static assets (favicons, manifests)
├── src/
│   ├── assets/            # Theme logos and diagrams
│   ├── content/docs/      # Markdown documentation pages
│   │   ├── intro/         # Getting started & quick start
│   │   ├── usage/         # CLI, Docker, API reference, GitHub Actions
│   │   ├── configuration/ # Envs, check methods, subscriptions, status page
│   │   ├── contributing/  # Development guide
│   │   ├── ru/            # Russian translation tree
│   │   └── fa/            # Persian / Farsi translation tree
│   └── styles/custom.css  # Custom CSS styles
├── astro.config.mjs       # Starlight configuration, sidebar, and locales
├── package.json           # Node.js dependencies and scripts
└── tsconfig.json          # TypeScript configuration
```

## 🚀 Local Development

To run the documentation site locally:

```bash
# Navigate to docs directory
cd docs

# Install dependencies
npm install

# Start local dev server (default: http://localhost:4321)
npm run dev

# Build production bundle to dist/
npm run build

# Preview production build locally
npm run preview
```

## 📝 Editing Guidelines

- When adding or updating configuration options, environment variables, or CLI arguments, update all three locale trees (`en`, `ru`, `fa`) synchronously.
- Use Starlight callouts (`:::note`, `:::tip`, `:::caution`) where appropriate.

# Lighthouse — Dashboard visual specification

## Reference and fidelity target

The dashboard follows the user's latest correction: a strict black-and-white interface with generous spacing and clear data hierarchy. Retain the local Bricolage font, considered type scale, and responsive spacing foundations established from House of ANIO, while using the monochrome colors below. Data presentation uses the thin marks and quiet grid lines of [Hairline figures](https://hairline.lucasmarkes.com/figures); data controls take cues from [shadcn data tables](https://ui.shadcn.com/docs/components/data-table) and [TanStack Table](https://tanstack.com/table/latest/docs/overview), implemented as native HTML and Go templates without React or JavaScript.

This specification is based on the current reference source files `app/globals.css`, `app/layout.tsx`, `components/site/site-header.tsx`, and `components/site/site-shell.tsx`. The dashboard adapts the same visual language to data tables, search, and record details; it should not copy fashion imagery or force an editorial two-column layout where a data view needs a list.

## Typography

- Headings: local **Bricolage Grotesque**, variable weights 200–800, loaded from a bundled local WOFF2 asset. Include the font's OFL license with the asset when implementation copies it.
- Body and UI: `system-ui, Arial, sans-serif`.
- Body baseline: `0.9375rem` (15px), line-height `1.65`, antialiased.
- Headings use the display face, weight 500, letter-spacing `-0.035em`, line-height `1.05`, balanced wrapping.
- Wordmark-style title: display face, weight 700, letter-spacing `-0.035em`; do not add a logo unless one is designed for Lighthouse.
- Navigation labels: `0.75rem`, letter-spacing `0.015em`.
- Form labels and helper text stay quiet and readable; use source gray/brown tokens rather than default browser blue.

## Color tokens

```css
:root {
  color-scheme: light;
  --brand: #111;
  --background: #fff;
  --foreground: #111;
  --white: #fff;
  --black: #111;
  --gray-100: #fafafa;
  --gray-200: #f5f5f5;
  --gray-300: #eee;
  --gray-400: #dedede;
  --gray-500: #bbb;
  --gray-600: #999;
  --gray-700: #777;
  --gray-800: #666;
  --gray-900: #5c5c5c;
  --gray-950: #333;
  --gray-1000: #444;
  --gray-1100: #333;
  --border: #dedede;
}
```

Use black for primary ink, actions, and selection. Use white for the canvas and controls; use only neutral grays for separators and secondary copy. Do not use burgundy, blue, or warm brown accents in the dashboard.

## Exact Lighthouse finish values

These values are user-selected for Lighthouse and intentionally override the current House of ANIO defaults. Preserve the decimals exactly in the initial theme configuration:

```css
:root {
  --finish-intensity: 0.44;
  --finish-halftone: 0.44;
  --finish-frosted: 0.39;
  --finish-glass: 0.33;
  --finish-grain: 0.43;
}
```

Keep a solid white canvas. These values remain part of the shared theme configuration but must not tint surfaces away from grayscale.

## Layout and spacing

- Content width: `min(100% - 2 * var(--page-gutter), 88rem)`.
- Page gutter: `clamp(1.25rem, 6vw, 6.25rem)`; at widths ≤680px use `1.25rem`.
- Main page sections use generous vertical spacing, consistent thin separators, and clear hierarchy. Avoid tight card grids and decorative borders around every row.
- Header: white, `z-index: 10`, clear thin border, no blur or translucent decoration.
- Header minimum height: `3.5rem`; horizontal nav spacing and alignment follow the reference. On narrow screens, use two rows with 2.8rem and 2.75rem tracks and horizontally scrollable navigation.
- Default radius: `0.2rem`. Use larger radii only where the reference uses them for controls; do not turn the interface into rounded-card UI.
- Borders: thin gray separators (`#dedede`), subtle shadows only for floating controls/popovers.

## Responsive behavior

- Preserve the reference breakpoints: 900px for compact navigation sizing; 760px for header reflow; 680px for narrow single-column behavior and reduced page gutter.
- Tables may switch to stacked record summaries on small screens; preserve the same typography and spacing rather than forcing horizontal overflow.
- Search and filter controls wrap cleanly; focus indication remains visible.

## Interaction and accessibility

- Text links use restrained animated underlines with a short ease-out transition, consistent with the reference.
- Focus-visible ring: 2px solid brand ink with 4px outline offset (3px inside the tuner control).
- Preserve keyboard navigation and semantic heading order.
- Respect `prefers-reduced-motion`; disable or sharply reduce transitions and animations.
- Provide adequate contrast for secondary text; finish effects are decorative only.

## Dashboard information architecture

1. **Overview:** metric cards, seven-day fine-line activity chart, local security summary, recent result table, audit table, and import/export controls.
2. **Results:** searchable result table, project filter, full document detail, editable core fields, and arbitrary per-document JSON fields.
3. **Memory:** scoped facts with inline edit controls, add form, and confirmed delete operation.
4. **Data tools:** JSON, Markdown, YAML, TOML, PDF, and SQLite snapshot import/export.

Dashboard writes use same-origin POST forms with a per-process CSRF token and security headers. Content is escaped by Go templates. Import data is bounded in size and merged rather than replacing the live database. The local dashboard has no user authentication; it remains bound to loopback.

Keep interaction surfaces compact. Data density should come from typographic hierarchy and spacing, not extra colors or card chrome.

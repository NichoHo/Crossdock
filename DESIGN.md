# Crossdock design revamp

This document is the complete specification for the Crossdock web UI revamp. It covers the console (`/console/*`), the login page (`/login`), and the scanner (`/scan`). Follow it exactly. When this document does not cover a case, use the closest rule in it. Do not invent a new style.

The visual reference is Attio (the CRM). Attio uses emoji as icons for lists and page titles. **Crossdock does not use emoji anywhere.** Every place where Attio shows an emoji, Crossdock shows a monotone Phosphor icon.

---

## 0. Rules for the implementer

Read this section first. Read it again before you mark the work as done.

1. **Change only the look.** Do not change data loading, server actions, URLs, query parameters, form field names, or business logic.
2. **Keep all user-visible text the same**, unless a page spec in section 8 tells you to change it. The e2e tests find elements by text.
3. **Keep these exactly as they are** (the e2e tests in `web/e2e/` use them):
   - Element ids: `#scan`, `#q-0`, `#file`, and every other `id` attribute.
   - Class names: `.chip`, `.timeline`, `.scan-head`, `.scan-body`.
   - Form field names: `writeoff_reason`, `resolution`, `note`, and all others.
   - Accessible button names: "Confirm", "Get next task", "Sync now", "Receive", "Return", "Ship", "Back", "Resolve", "Replay now", "Good", "Create endpoint", "Confirm return", "Submit count", "Upload and match", "Accept and create relocate tasks", and all others.
   - Heading text: every current `<h1>` and `<h2>` text (for example "Slotting", "Busiest locations", "Reconciliation workbench").
   - The visible texts "Online", "Offline", and "N unsynced" in the scanner sync bar.
   - The floor map SVG `role="img"` and its `aria-label`.
4. **Icons are decoration.** Every icon inside a button, link, or label gets `aria-hidden="true"`. An icon never replaces the text of a button, except where this document says "icon button". An icon button always has an `aria-label`.
5. **No emoji.** Not in UI text, not in empty states, not in comments that render, not in page titles.
6. **No inline `style={{...}}` props**, except for values that the code computes at runtime (SVG `x`, `y`, `width`, `height`). Section 11 lists every current inline style and its replacement class.
7. **One stylesheet.** All CSS lives in `web/src/app/globals.css`. Do not add CSS modules, Tailwind, styled-components, or another UI library. Do not add npm packages. Phosphor (`@phosphor-icons/react`) is already installed.
8. **Use tokens only.** No raw hex colors in TSX or in CSS outside the token blocks at the top of `globals.css`.
9. **Work in the order of section 12.** Run the checks in section 13 after each phase.

---

## 1. Design direction

### 1.1 What we take from Attio

| Attio trait | How Crossdock uses it |
|---|---|
| Light grey sidebar, white content area, thin hairline borders | Same. Sidebar `--bg-subtle`, content `--bg`, borders `--border` (1px). |
| Workspace switcher at the top of the sidebar | The warehouse switcher moves into this slot. |
| Grouped sidebar navigation with small section titles ("Records", "Lists") | Navigation groups "Operations", "Control", "System". |
| Emoji before list names and page titles | **Replaced** by Phosphor icons, monotone, `--text-secondary`. |
| A slim page bar with the page name, then a toolbar row (view switcher left, actions right) | Every console page: `PageHeader` bar, then `.toolbar`. |
| Data tables with column type icons in the header, vertical and horizontal hairlines, no zebra rows, right-aligned numbers | Same. See `.table-wrap` and the `Th` component. |
| A footer row in the table with the row count | `TableFoot` component with the count and the next-page link. |
| Soft tinted pills for status ("Active", "Enterprise") | `StatusChip` becomes a tinted pill. It keeps its shape glyph. |
| Kanban board with column headers (dot, title, count) and white cards with property rows | The Tasks page. |
| Inter type, 14px body, medium (500) weight for UI labels | Same. |
| Buttons: 32px tall, 8px radius, white with a border and a faint shadow; one blue primary | Same. |

### 1.2 What we do not take from Attio

| Attio trait | Why not |
|---|---|
| Emoji icons | The owner does not want them. They also break the monotone look. |
| Multi-colour category tags (blue "Enterprise", yellow "Pro") | In Crossdock, colour means status. Non-status categories use the neutral `.tag`. |
| Light grey secondary text near 4.5:1 contrast | Crossdock keeps 7:1 for text. `--text-secondary` is darker than Attio's grey. |
| Avatars, comments, AI sparkle buttons, "Quick actions" search | Crossdock has no such features. Do not add them. |

### 1.3 Principles kept from `CROSSDOCK_SPEC.md` section 12

Spec section 12 is updated to match this document. These principles from it stay:

1. **Contrast.** Body text and codes meet 7:1 on their background. Secondary text meets 7:1 on `--bg`, `--bg-subtle`, and `--bg-hover`. Section 3 gives the measured values.
2. **State by shape and colour, never colour alone.** `StatusChip` keeps its shape glyph (circle, triangle, octagon, diamond, outline circle).
3. **Codes are monospace, always.** SKU, LPN, location, lot, tracking number, ids, quantities, money, timestamps.
4. **Scanner touch targets.** 48px minimum, 56px for primary actions. The console is a desk tool, so its controls are 32px. On a touch screen (`pointer: coarse`) console controls grow to 44px.
5. **Offline state is always visible** on the scanner.
6. **Dark theme is a first-class theme.** Every token has a dark value.
7. **Motion is minimal.** 120ms colour transitions only. The scan flash stays.

---

## 2. Theme

There are two themes: light and dark. The system preference selects the theme. `data-theme="light"` or `data-theme="dark"` on `<html>` forces a theme. The token blocks in Appendix A implement this. Do not add a theme toggle.

---

## 3. Colour tokens

All colours are CSS custom properties on `:root`. The full CSS is in Appendix A.

### 3.1 Surfaces and lines

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#FFFFFF` | `#161618` | Content area, cards, tables, inputs, buttons |
| `--bg-subtle` | `#FAFAFA` | `#111113` | Sidebar, table header row, table footer, board columns, login backdrop, scanner backdrop |
| `--bg-hover` | `#F4F4F5` | `#232327` | Hover on rows, nav items, ghost buttons; neutral tag fill |
| `--bg-active` | `#EFEFF1` | `#2A2A2E` | Current nav item |
| `--border` | `#E9E9EC` | `#2A2A2E` | All hairlines: cards, tables, dividers |
| `--border-strong` | `#D6D7DB` | `#3A3A40` | Input borders, secondary button borders, dashed empty state |

### 3.2 Text

| Token | Light | Dark | Use | Contrast (measured) |
|---|---|---|---|---|
| `--text` | `#1C1D1F` | `#EDEDEF` | Body text, codes, headings | 16.9:1 light, 15.5:1 dark |
| `--text-secondary` | `#505259` | `#A1A3AA` | Labels, table headers, hints, nav icons | 7.8:1 on `--bg`, 7.5:1 on `--bg-subtle` (light); 7.2:1 on `--bg` (dark) |
| `--text-placeholder` | `#8A8C93` | `#6E7079` | Placeholder text, empty state icon only. Never for real content. | 3.4:1 (placeholder only) |

### 3.3 Accent

| Token | Light | Dark | Use |
|---|---|---|---|
| `--accent` | `#2563EB` | `#6EA8FF` | Primary button fill, links, focus ring, selection, floor map heat |
| `--accent-hover` | `#1D4ED8` | `#8FBCFF` | Primary button hover |
| `--accent-soft` | `#EFF4FF` | `#172036` | Selected row or list item background |
| `--on-accent` | `#FFFFFF` | `#0B0B0C` | Text on `--accent` (5.2:1 light, 8.2:1 dark) |

### 3.4 Status

Status colours are used **only** for status: `StatusChip`, banners, the ageing bar, map move markers, and the scanner flash. Do not use them for decoration.

| Tone | Text token | Light | Dark | Tint token | Light | Dark | Shape |
|---|---|---|---|---|---|---|---|
| ok | `--ok` | `#166534` | `#4ADE80` | `--ok-bg` | `#ECFDF3` | `#16271D` | filled circle |
| attention | `--attention` | `#854D0E` | `#FACC15` | `--attention-bg` | `#FEF7E0` | `#2A2410` | filled triangle |
| stop | `--stop` | `#991B1B` | `#FCA5A5` | `--stop-bg` | `#FEF2F2` | `#2D1717` | filled octagon |
| hold | `--hold` | `#5B21B6` | `#C4B5FD` | `--hold-bg` | `#F5F3FF` | `#221B33` | filled diamond |
| pending | `--pending` | `#505259` | `#A1A3AA` | `--pending-bg` | `#F4F4F5` | `#232327` | outline circle |

Measured contrast of text on its own tint: ok 6.8:1, attention 6.4:1, stop 7.6:1, hold 8.2:1 (light); all above 8.8:1 in dark. Chip labels are 12px medium, so they are labels, not body text.

### 3.5 Old token names

The old tokens (`--surface`, `--surface-raised`, `--ink`, `--ink-muted`, `--line`, `--action`) are deleted. Replace every use in TSX:

| Old | New |
|---|---|
| `var(--surface)` | `var(--bg-subtle)` |
| `var(--surface-raised)` | `var(--bg)` |
| `var(--ink)` | `var(--text)` |
| `var(--ink-muted)` | `var(--text-secondary)` |
| `var(--line)` | `var(--border)` |
| `var(--action)` | `var(--accent)` |

After phase 1, `grep -rn "\-\-ink\|\-\-line\|\-\-action\|\-\-surface" web/src` must return nothing.

---

## 4. Typography

### 4.1 Faces

| Face | Use | Loading |
|---|---|---|
| **Inter** (variable) | All UI text | `next/font/google`, variable `--font-inter` |
| **JetBrains Mono** 400, 500, 600 | Every code, id, quantity, money value, timestamp | `next/font/google`, variable `--font-jetbrains` |

Inter replaces Atkinson Hyperlegible. To keep the legibility that Atkinson gave, the body turns on two Inter character variants: `cv05` (lower-case l with a tail) and `cv08` (upper-case I with serifs). This makes `l`, `I`, and `1` distinct. Codes still use JetBrains Mono, which has a slashed zero.

### 4.2 Console type scale

| Role | Size / line height | Weight | Face | Colour | Class or element |
|---|---|---|---|---|---|
| Page title (in the page bar) | 15 / 20 | 600 | Inter | `--text` | `h1` inside `.page-head` |
| Record title (detail pages, a code) | 15 / 20 | 600 | JetBrains | `--text` | `h1.code` inside `.page-head` |
| Section heading | 14 / 20 | 600 | Inter | `--text` | `h2` |
| Sub-heading | 13 / 18 | 600 | Inter | `--text-secondary` | `h3` |
| Body, table cells, buttons | 14 / 20 | 400 (buttons 500) | Inter | `--text` | `body` |
| Label, table header, nav group title | 13 / 18 (group title 12) | 500 | Inter | `--text-secondary` | `.label`, `th`, `.nav-group-title` |
| Small secondary line | 12 / 16 | 400 | Inter | `--text-secondary` | `.sub` |
| Code in text or a table | 13 / 20 | 500 | JetBrains | `--text` | `.code`, `code` |
| Number in a table | 13 / 20 | 400 | JetBrains | `--text` | `.num` |
| KPI value | 22 / 28 | 600 | JetBrains | `--text` | `.kpi dd`, `.ageing .n` |
| Big stat (slotting saving) | 28 / 36 | 600 | JetBrains | `--text` | `.stat` |
| Chip and tag text | 12 / 16 | 500 | Inter | tone colour | `.chip`, `.tag` |

Labels are **sentence case**. Do not use `text-transform: uppercase` anywhere. Do not use letter spacing on labels.

### 4.3 Scanner type scale

The scanner keeps larger sizes for glare and distance. Nothing on the scanner is below 14px.

| Role | Size | Weight | Face |
|---|---|---|---|
| Code in focus (`.scan-head .code`, `.scan-target`) | 28 | 600 | JetBrains |
| Secondary code line (`.code.code-m`) | 18 | 600 | JetBrains |
| Code in a list (`.code.code-s`) | 16 | 600 | JetBrains |
| Item code on a card (`.code.code-l`) | 24 | 600 | JetBrains |
| Body | 18 | 400 | Inter |
| Label (`.scan .label`) | 14 | 600 | Inter |
| Primary button | 18 | 600 | Inter |
| Quantity in stepper | 40 | 600 | JetBrains |
| Scan input | 24 | 600 | JetBrains |

---

## 5. Iconography

### 5.1 Rules

1. **Library:** Phosphor only (`@phosphor-icons/react`). No other icon set, no inline SVG icons, no emoji, no icon fonts.
2. **Import path:**
   - In server components (no `"use client"` at the top): `import { Package } from "@phosphor-icons/react/ssr";`
   - In client components (`"use client"`): `import { Package } from "@phosphor-icons/react";`
   - Shared files that both kinds import (`src/lib/nav.ts`, `src/components/Banner.tsx`, `src/components/EmptyState.tsx`, `src/components/Table.tsx`, `src/components/PageHeader.tsx`): use `@phosphor-icons/react/ssr`. These files have no hooks, so the SSR build works on both sides.
3. **Weight:** `regular` in the console and on the login page (omit the `weight` prop). `bold` on the scanner, for glare. Exception: the scanner step check uses `weight="fill"` (it is a status mark).
4. **Colour:** always `currentColor` (omit the `color` prop). The parent sets the colour. Default icon colour is `--text-secondary`. Do not colour icons with status tokens, except inside `StatusChip`, banners, and the scanner step check.
5. **Sizes:**

| Place | Size |
|---|---|
| Sidebar nav item | 16 |
| Page bar icon tile | 14 (inside a 24px tile) |
| Brand mark | 14 (inside a 24px tile) |
| Table header (`Th`) | 14 |
| Button with text (console) | 16 |
| Small button (console) | 14 |
| Banner | 16 |
| Empty state | 20 |
| Card property row | 14 |
| Scanner button | 22 |
| Scanner sync bar | 18 |
| Scanner step check | 26 |

6. **Spacing:** icon to text gap is 8px in nav items and 6px everywhere else. The CSS handles this with `gap`. Do not add spaces or margins.
7. **Monotone tiles replace emoji.** Where Attio shows an emoji before a page or list name, Crossdock shows `.icon-tile`: a 24px rounded square with a 1px border, `--bg` fill, and the section icon at 14px in `--text-secondary`.

### 5.2 Section icons

One icon per section. The sidebar, the page bar, and any link to the section use the same icon. `src/lib/nav.ts` holds this map (section 7.1).

| Section key | Label in sidebar | Route | Icon |
|---|---|---|---|
| `stock` | Stock | `/console/stock` | `Package` |
| `tasks` | Tasks | `/console/tasks` | `ListChecks` |
| `receiving` | Receiving | `/console/receiving` | `Truck` |
| `orders` | Orders & waves | `/console/orders` | `ShoppingCart` |
| `returns` | Returns | `/console/returns` | `ArrowUUpLeft` |
| `reconciliation` | Reconciliation | `/console/reconciliation` | `Scales` |
| `settlement` | Settlement | `/console/settlement` | `Coins` |
| `slotting` | Slotting | `/console/slotting` | `ArrowsOutCardinal` |
| `map` | Floor map | `/console/map` | `MapTrifold` |
| `timeline` | Timeline | `/console/timeline` | `ClockCounterClockwise` |
| `endpoints` | Webhooks | `/console/endpoints` | `WebhooksLogo` |
| (footer link) | Scanner | `/scan` | `Barcode` |
| (brand) | Crossdock | none | `Warehouse` |

### 5.3 Column type icons

Every table header cell shows a type icon, like Attio. Use the `Th` component (section 7.6). The `type` prop selects the icon.

| `type` | Use for | Icon | Alignment |
|---|---|---|---|
| `text` | Free text: reason, detail, why, decision | `TextAa` | left |
| `code` | SKU, LPN, lot, order id, ref, tracking, event id, task id, topic, file name | `Barcode` | left |
| `location` | Location, zone, from, to | `MapPin` | left |
| `party` | Supplier, courier, operator | `Buildings` | left |
| `status` | State, status, breaker, result, proposed | `CircleHalf` | left |
| `link` | URL | `LinkSimple` | left |
| `date` | Dispatched, received (timestamps) | `CalendarBlank` | left |
| `num` | Quantities and counts | `Hash` | right |
| `money` | Money amounts | `CurrencyCircleDollar` | right |
| `age` | Age in days, "pays within" | `Hourglass` | right |
| `action` | A column of buttons or links with no header text | no icon | left |

### 5.4 Other icons

| Purpose | Icon |
|---|---|
| Warehouse switcher caret | `CaretUpDown` |
| Breadcrumb separator | `CaretRight` |
| Next page / older items | `CaretRight` (trailing) |
| Filter submit button | `Funnel` |
| Error banner | `WarningOctagon` |
| Success banner | `CheckCircle` |
| Info banner | `Info` |
| Empty state | `Tray` |
| User in sidebar footer | `UserCircle` |
| Sign out | `SignOut` |
| Run / start a job (sweep, schedule, reconcile, find unpaid, replay) | `Play` |
| Upload | `UploadSimple` |
| Accept | `Check` |
| Reject, cancel, disable | `Prohibit` |
| Rotate secret | `Key` |
| Assign | `UserPlus` |
| History / timeline link in a row | `ClockCounterClockwise` |
| Show on floor map link | `MapTrifold` |
| Scanner online | `WifiHigh` (already used) |
| Scanner offline | `WifiSlash` (already used) |
| Scanner sync now | `ArrowsClockwise` (already used) |
| Scanner Receive | `Truck` |
| Scanner Return | `ArrowUUpLeft` |
| Scanner Ship | `TruckTrailer` |
| Scanner Get next task | `ArrowRight` (trailing) |
| Scanner Back | `ArrowLeft` |
| Scanner step done | `CheckCircle` weight fill (already used) |

All icon names in this document exist in the installed Phosphor version. Do not substitute other names.

---

## 6. Layout

### 6.1 Spacing scale

Use only these values for padding, margin, and gap: **2, 4, 6, 8, 10, 12, 16, 20, 24, 28, 32, 40** px.

| Where | Value |
|---|---|
| Between top-level blocks on a page (`.page-body` gap) | 16 |
| Inside a panel (`.panel` padding, gap) | 16, 12 |
| Toolbar items | 8 |
| Table cell padding | 6 vertical, 12 horizontal |
| Page horizontal padding | 20 (16 on narrow screens) |
| Sidebar padding | 8 |

### 6.2 Radius

| Token | Value | Use |
|---|---|---|
| `--r-sm` | 4px | Tags, count badges, checkboxes |
| `--r-md` | 6px | Chips, small buttons, small inputs, nav items, tabs inside the tab bar, icon tiles |
| `--r-lg` | 8px | Buttons, inputs, tab bar, table container, banners, cards on the board |
| `--r-xl` | 12px | Panels, KPI cards, empty states, board columns, login card, all scanner controls |

### 6.3 Elevation

| Token | Light | Dark | Use |
|---|---|---|---|
| `--shadow-xs` | `0 1px 2px rgba(16,17,19,0.06)` | `none` | Buttons, inputs, cards, panels, icon tiles |
| `--shadow-sm` | `0 1px 3px rgba(16,17,19,0.08), 0 1px 2px rgba(16,17,19,0.04)` | `0 0 0 1px var(--border)` | Selected tab, login card |

No other shadows. No blur, no glass, no gradients (except the ageing stripe pattern).

### 6.4 Console shell

```
+--------------+------------------------------------------------------------+
| [W] Main  ⇅  | [icon] Page title                         [page actions]  |  48px page bar (sticky)
|              +------------------------------------------------------------+
| [] Stock     |                                                            |
| [] Tasks     |  toolbar: [tabs ............]            [secondary acts]  |
|              |                                                            |
| Operations   |  +------------------------------------------------------+  |
| [] Receiving |  | th icon Label | th icon Label | ...                  |  |
| [] Orders    |  |---------------+---------------+-----------------------|  |
| [] Returns   |  | row           |               |                       |  |
|              |  |------------------------------------------------------|  |
| Control      |  | 50 rows, more available                 [Next page >] |  |
| ...          |  +------------------------------------------------------+  |
|              |                                                            |
| System       |                                                            |
| ...          |                                                            |
|--------------|                                                            |
| [] Scanner   |                                                            |
| (o) user  ->]|                                                            |
+--------------+------------------------------------------------------------+
   240px                               fluid
```

- `.shell`: CSS grid, columns `240px` and `minmax(0, 1fr)`. Full viewport height.
- `.sidebar`: sticky, full height, scrolls on its own, `--bg-subtle`, right border.
- `.main`: holds the page. Each page renders `PageHeader` and then `<div className="page-body">`.
- `.page-head`: 48px, sticky at the top of `.main`, bottom border, `--bg`.
- `.page-body`: padding `16px 20px 32px`. A flex column with a 16px gap, so blocks are spaced the same on every page. Do not add margins between top-level blocks.

### 6.5 Narrow screens (max-width 800px)

- The shell becomes one column.
- The sidebar becomes a horizontal strip at the top. It scrolls sideways. Group titles hide. The warehouse switcher, nav items, scanner link, and user row sit in one row.
- `.split` becomes one column.
- `.ageing` becomes two columns.
- Page padding becomes 16px.

### 6.6 Touch screens (`pointer: coarse`)

Console controls grow: buttons 44px, small buttons 40px, inputs 44px with 16px text (this stops iOS zoom), nav items 44px, tabs 40px, checkboxes 24px. The scanner already meets this.

---

## 7. Components

All CSS is in Appendix A. This section says what each component is, when to use it, and the TSX shape.

### 7.1 `src/lib/nav.ts` (new)

The single source for sections, labels, routes, icons, and groups.

```ts
import type { Icon } from "@phosphor-icons/react";
import {
  Package, ListChecks, Truck, ShoppingCart, ArrowUUpLeft, Scales, Coins,
  ArrowsOutCardinal, MapTrifold, ClockCounterClockwise, WebhooksLogo,
} from "@phosphor-icons/react/ssr";

export const SECTIONS = {
  stock: { href: "/console/stock", label: "Stock", icon: Package },
  tasks: { href: "/console/tasks", label: "Tasks", icon: ListChecks },
  receiving: { href: "/console/receiving", label: "Receiving", icon: Truck },
  orders: { href: "/console/orders", label: "Orders & waves", icon: ShoppingCart },
  returns: { href: "/console/returns", label: "Returns", icon: ArrowUUpLeft },
  reconciliation: { href: "/console/reconciliation", label: "Reconciliation", icon: Scales },
  settlement: { href: "/console/settlement", label: "Settlement", icon: Coins },
  slotting: { href: "/console/slotting", label: "Slotting", icon: ArrowsOutCardinal },
  map: { href: "/console/map", label: "Floor map", icon: MapTrifold },
  timeline: { href: "/console/timeline", label: "Timeline", icon: ClockCounterClockwise },
  endpoints: { href: "/console/endpoints", label: "Webhooks", icon: WebhooksLogo },
} satisfies Record<string, { href: string; label: string; icon: Icon }>;

export type SectionKey = keyof typeof SECTIONS;

/** Sidebar groups. A group with an empty title renders without a title row. */
export const NAV_GROUPS: { title: string; items: SectionKey[] }[] = [
  { title: "", items: ["stock", "tasks"] },
  { title: "Operations", items: ["receiving", "orders", "returns"] },
  { title: "Control", items: ["reconciliation", "settlement", "slotting", "map"] },
  { title: "System", items: ["timeline", "endpoints"] },
];
```

### 7.2 Sidebar: `src/components/ConsoleNav.tsx` (rewrite)

Client component (it uses `usePathname`). It receives the warehouse list and the session from the layout.

Props: `{ warehouses: { id: string; name: string }[]; wid: string; user: string; role: string }`.

Structure:

```tsx
<nav className="sidebar" aria-label="Sections">
  <WarehouseSwitcher warehouses={warehouses} wid={wid} />
  {NAV_GROUPS.map((g) => (
    <div className="nav-group" key={g.title || "top"}>
      {g.title && <div className="nav-group-title">{g.title}</div>}
      {g.items.map((k) => {
        const { href, label, icon: Icon } = SECTIONS[k];
        return (
          <Link key={href} href={href} className="nav-item" aria-current={path.startsWith(href) ? "page" : undefined}>
            <Icon size={16} aria-hidden="true" />
            {label}
          </Link>
        );
      })}
    </div>
  ))}
  <div className="sidebar-foot">
    <Link href="/scan" className="nav-item"><Barcode size={16} aria-hidden="true" />Scanner</Link>
    <div className="user-row">
      <UserCircle size={20} aria-hidden="true" />
      <span className="user-name">{user}</span>
      <span className="tag">{role}</span>
      <SignOutButton className="btn ghost small icon" iconOnly />
    </div>
  </div>
</nav>
```

`Barcode` and `UserCircle` come from `@phosphor-icons/react` (client import path).

### 7.3 Warehouse switcher: `src/components/WarehouseSwitcher.tsx` (new)

This replaces the top bar form. It looks like Attio's workspace switcher: the brand mark, the warehouse name, and a caret. A native `<select>` sits on top of it with `opacity: 0`, so it stays accessible and keyboard friendly. The form submits when the value changes.

```tsx
"use client";
import { CaretUpDown, Warehouse } from "@phosphor-icons/react";
import { selectWarehouse } from "@/app/actions";

export function WarehouseSwitcher({ warehouses, wid }: { warehouses: { id: string; name: string }[]; wid: string }) {
  const current = warehouses.find((w) => w.id === wid);
  return (
    <form action={selectWarehouse} className="ws">
      <span className="brand-mark"><Warehouse size={14} weight="bold" aria-hidden="true" /></span>
      <span className="ws-text">
        <span className="ws-name">{current?.name ?? "No warehouse"}</span>
        <span className="ws-id code">{current?.id ?? "Crossdock"}</span>
      </span>
      <CaretUpDown size={14} aria-hidden="true" className="ws-caret" />
      <select name="wid" defaultValue={wid} aria-label="Warehouse" onChange={(e) => e.currentTarget.form?.requestSubmit()}>
        {warehouses.map((w) => <option key={w.id} value={w.id}>{w.name} ({w.id})</option>)}
      </select>
    </form>
  );
}
```

The brand mark is the one place with a filled dark tile: `--text` fill, `--bg` icon. This is monotone and matches Attio's black logo tile.

### 7.4 Page bar: `src/components/PageHeader.tsx` (new)

Server component. Every console page renders it first. It holds the only `<h1>` of the page.

Props:

| Prop | Type | Meaning |
|---|---|---|
| `section` | `SectionKey` | Selects the icon. |
| `title` | `string` | The `<h1>` text. Use the exact current heading text. |
| `parent` | `boolean` (optional) | When true, shows a breadcrumb link to the section before the title (detail pages). |
| `code` | `boolean` (optional) | When true, the title is a code (mono). |
| `actions` | `React.ReactNode` (optional) | Buttons on the right side of the bar. |

```tsx
<header className="page-head">
  <span className="icon-tile"><Icon size={14} aria-hidden="true" /></span>
  {parent && (
    <>
      <Link className="crumb" href={s.href}>{s.label}</Link>
      <CaretRight size={12} className="crumb-sep" aria-hidden="true" />
    </>
  )}
  <h1 className={code ? "code" : undefined}>{title}</h1>
  {actions && <div className="actions">{actions}</div>}
</header>
```

### 7.5 Toolbar and tabs

**`.toolbar`** is a flex row below the page bar. Left side: tabs or filters. Then `<span className="spacer" />`. Right side: secondary action buttons. Every page with filters or page-level actions has exactly one toolbar as the first child of `.page-body`.

**`Tabs`** (`src/components/Tabs.tsx`, new, server-safe, no hooks) replaces every row of `btn small` / `btn small secondary` filter links. It is a segmented control, like the Attio view switcher.

```tsx
import Link from "next/link";

export function Tabs({ label, items }: { label: string; items: { href: string; label: string; active: boolean; count?: number }[] }) {
  return (
    <nav className="tabs" aria-label={label}>
      {items.map((t) => (
        <Link key={t.href} href={t.href} aria-current={t.active ? "page" : undefined}>
          {t.label}
          {t.count !== undefined && <span className="badge">{t.count}</span>}
        </Link>
      ))}
    </nav>
  );
}
```

Keep the current `aria-label` of each filter nav ("Filter by state", "Settlement sections", "Workbench sections", "Period", "Filter by status") as the `label` prop. Keep the link texts.

### 7.6 Tables: `src/components/Table.tsx` (new)

Two server-safe components: `Th` and `TableFoot`.

```tsx
import Link from "next/link";
import { TextAa, Barcode, MapPin, Buildings, CircleHalf, LinkSimple, CalendarBlank, Hash, CurrencyCircleDollar, Hourglass, CaretRight } from "@phosphor-icons/react/ssr";

const ICONS = { text: TextAa, code: Barcode, location: MapPin, party: Buildings, status: CircleHalf, link: LinkSimple, date: CalendarBlank, num: Hash, money: CurrencyCircleDollar, age: Hourglass } as const;
const RIGHT = new Set(["num", "money", "age"]);

export type ColType = keyof typeof ICONS | "action";

export function Th({ type, children }: { type: ColType; children?: React.ReactNode }) {
  const Icon = type === "action" ? null : ICONS[type];
  return (
    <th className={RIGHT.has(type) ? "num" : type === "action" ? "action" : undefined}>
      <span className="th-in">{Icon && <Icon size={14} aria-hidden="true" />}{children}</span>
    </th>
  );
}

/** Footer row inside .table-wrap: row count on the left, next page link on the right. */
export function TableFoot({ count, nextHref, nextLabel = "Next page", children }: {
  count: number; nextHref?: string; nextLabel?: string; children?: React.ReactNode;
}) {
  return (
    <div className="table-foot">
      <span>{children ?? <>{count} {count === 1 ? "row" : "rows"}{nextHref ? ", more available" : ""}</>}</span>
      {nextHref && (
        <Link className="btn secondary small" href={nextHref}>
          {nextLabel}<CaretRight size={14} aria-hidden="true" />
        </Link>
      )}
    </div>
  );
}
```

Table rules:

1. Every table sits in `<div className="table-wrap">`. `TableFoot` goes **inside** `.table-wrap`, after `</table>`.
2. Every `<th>` is a `Th` with a `type` from section 5.3.
3. Codes in cells: `<td className="code">`. Numbers: `<td className="num">`. Money: `<td className="num">`.
4. A second line in a cell (detail, supplier, lot, courier ref) is `<span className="sub">` after the first line. Delete the `<br />` (`.sub` is `display: block`).
5. A link inside a code cell keeps `className="code"` on the `td`. The link inherits the mono font.
6. A link column (History, Timeline) uses `Th type="action"` and a ghost small button with an icon: `<Link className="btn ghost small" href=...><ClockCounterClockwise size={14} aria-hidden="true" />History</Link>`.
7. Inline forms inside cells use `.input.small` inputs and selects and `btn small` buttons, in a `.row`.
8. A selected row (for example the open ASN on Receiving) gets `aria-current="true"` on its `<tr>`.
9. Checkbox column: `<td className="check">` and `<Th type="action">` with the existing sr-only label inside. Delete the inline checkbox size style. The CSS sizes checkboxes.

### 7.7 Buttons

| Class | Look | Use |
|---|---|---|
| `.btn` | Accent fill, `--on-accent` text | The one primary action in a view (submit a form, release wave, accept) |
| `.btn.secondary` | `--bg` fill, `--border-strong` border, `--text` text | Most actions: run jobs, filters, next page, cancel |
| `.btn.danger` | `--bg` fill, `--stop` text; hover tints `--stop-bg` | Destructive actions: cancel order, reject, disable |
| `.btn.ghost` | No fill, no border, `--text-secondary` text | Row links, sign out, low-priority actions |
| `.btn.small` | 28px tall, 13px text | Inside tables, cards, toolbars with many items |
| `.btn.icon` | Square | Icon-only button. Needs `aria-label`. |

Sizes: default 32px tall, 12px side padding, 8px radius, 14px weight 500. Icons inside buttons are 16px (14px in `.small`) and come **before** the text, except "Next page" style links and "Get next task", where a trailing caret or arrow comes **after** the text.

Do not put more than one `.btn` (primary) in the same toolbar, panel, or form.

`SubmitButton` (in `ActionForm.tsx`) keeps its behaviour. It accepts `children` that may include an icon. Its pending text stays "Working...".

### 7.8 Form fields

- `.field`: label above control, 6px gap, 12px bottom margin.
- `.label`: 13px, weight 500, `--text-secondary`, sentence case.
- Inputs, selects, file inputs inside `.field` or `.filters`, and any element with `.input`: 32px tall, 1px `--border-strong` border, 8px radius, `--bg` fill, `--shadow-xs`. Focus: accent border plus a 3px accent ring at 20% opacity.
- `.input.small`: 28px tall, 6px radius, 13px text. Use inside tables and cards.
- Code inputs keep `className="code"` (mono, 13px).
- Hints under a field: `<span className="hint">`.
- **Filter forms** (`.filters`): labelled inputs in a row, then a secondary submit button with a `Funnel` icon: `<button className="btn secondary" type="submit"><Funnel size={16} aria-hidden="true" />Filter</button>`. The filter form goes inside the page toolbar position (first child of `.page-body`).

### 7.9 Status chip: `src/components/StatusChip.tsx` (restyle)

Keep the `TONES` map, the `toneFor` function, the `SHAPES`, and the props. Change:

1. Default `size` from 14 to 10.
2. The chip is now a tinted pill: 22px tall, 8px side padding, 6px radius, 12px weight 500, tone text colour on tone tint. The CSS does this.
3. Export the shape as its own component, for board column headers:

```tsx
export function StatusShape({ state, size = 10 }: { state: string; size?: number }) {
  const tone = toneFor(state);
  return (
    <svg className="shape" data-tone={tone} width={size} height={size} viewBox="0 0 14 14" aria-hidden="true">{SHAPES[tone]}</svg>
  );
}
```

4. **Every status display uses `StatusChip`.** Replace the hand-written `<span className="chip" data-tone="stop">...</span>` in settlement and reconciliation with `<StatusChip state="exception" label={...} />`. This adds the octagon shape, which the bare span lacked. The `.chip` class stays on the element, so the e2e test still finds it.

### 7.10 Tag (neutral)

`<span className="tag">` is a neutral, non-status label: task kind, role, courier, zone, event topic, return reason, condition. 22px tall, 4px radius, `--bg-hover` fill, 1px `--border`, 12px weight 500, `--text`. Add `code` for code values: `<span className="tag code">`.

Tags are always neutral grey. Never colour a tag.

### 7.11 Count badge

`<span className="badge">` shows a count next to a title or tab. 20px tall, mono 12px, `--bg-hover` fill, `--text-secondary`. Use it on board column headers and tabs.

### 7.12 Banner: `src/components/Banner.tsx` (new)

Replaces every `<div className="banner-error" role="alert">`. Server-safe, no hooks, so the scanner can use it too.

```tsx
import { WarningOctagon, CheckCircle, Info } from "@phosphor-icons/react/ssr";

const ICON = { error: WarningOctagon, ok: CheckCircle, info: Info } as const;

export function Banner({ tone = "error", role = tone === "error" ? "alert" : "status", children }: {
  tone?: keyof typeof ICON; role?: "alert" | "status"; children: React.ReactNode;
}) {
  const Icon = ICON[tone];
  return (
    <div className={`banner banner-${tone}`} role={role}>
      <Icon size={16} weight="bold" aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
}
```

Use `tone="error"` for errors, `tone="ok"` for success messages, `tone="info"` for notes. In `ActionForm`, the error becomes `<Banner>`, and the success message `state.ok` becomes `<Banner tone="ok">`. Where a current banner uses `role="status"` (endpoint detail), pass `role="status"`.

### 7.13 Empty state: `src/components/EmptyState.tsx` (new)

Replaces every `<div className="empty">`. Keep the text.

```tsx
import { Tray } from "@phosphor-icons/react/ssr";

export function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="empty">
      <Tray size={20} aria-hidden="true" />
      <p>{children}</p>
    </div>
  );
}
```

Dashed `--border-strong` border, `--bg-subtle` fill, 12px radius, 40px vertical padding, centred `--text-secondary` text.

### 7.14 Panel

`.panel` is a white card: 1px `--border`, 12px radius, 16px padding, `--shadow-xs`. It is a flex column with a 12px gap, so headings and paragraphs inside it need no margins. Use it for forms, detail blocks, and the map. A `<details className="panel">` uses its `<summary>` as the title row (weight 600).

Width helper: `.narrow` sets `max-width: 560px`. Use it on every standalone form panel.

### 7.15 KPI cards

```tsx
<dl className="kpis" aria-label="Money position">
  <div className="kpi">
    <dt>Label</dt>
    <dd>{value}</dd>
    <span className="sub">hint</span>
  </div>
</dl>
```

Used for the Settlement money position. The value is mono 22px weight 600.

### 7.16 Ageing bar (restyle, keep markup)

Keep the current markup (`.ageing`, children `.b0` to `.b3`, `.n`, `.lbl`). Each bucket is now a KPI-style card with a coloured bar on its left edge. The bar is the non-colour signal: `b0` and `b1` 4px solid, `b2` an 8px striped bar, `b3` a 6px solid bar. The number is in `--text`, not in the tone colour.

### 7.17 Board (Tasks page)

Attio kanban, image 4 style.

```tsx
<div className="board">
  <section className="board-col" aria-labelledby={`col-${state}`}>
    <header className="board-col-head">
      <StatusShape state={state} />
      <h2 id={`col-${state}`}>{title}</h2>
      <span className="badge">{count}{more ? "+" : ""}</span>
    </header>
    {items.length === 0 && <p className="board-empty">Nothing here.</p>}
    <details className="card">
      <summary>
        <span className="row"><span className="tag">{kind}</span><StatusChip state={t.state} /></span>
        <span className="code">{t.from_location || t.sku || t.id}</span>
      </summary>
      <div className="card-body">
        <dl className="props">
          <dt><Barcode size={14} aria-hidden="true" />Task</dt><dd className="code">{t.id}</dd>
          ...
        </dl>
        {assign form}
      </div>
    </details>
  </section>
</div>
```

Columns are 280px minimum, `--bg-subtle`, 12px radius. The board scrolls sideways when the columns do not fit. Cards are white, 8px radius, `--shadow-xs`. Card property icons follow the column type icons (5.3): Task `Barcode`, SKU x qty `Hash`, From and To `MapPin`, Order `Barcode`, Assigned to `Buildings`, Reason `TextAa`.

### 7.18 Split layout and list

`.split` is a two-column grid: list on the left (280px to 360px), detail on the right. Used on Receiving and Slotting.

`.list` is a bordered card of rows. Each row is `<li><Link className="list-item" aria-current={selected ? "true" : undefined}>`. The selected row gets `--accent-soft` and a 2px accent bar on its left edge.

### 7.19 Record head (detail pages)

Detail pages show the record id in the page bar (`PageHeader` with `parent` and `code`). Below it, `.record-meta` is a row of chips and secondary facts (URL, counts). It replaces the current chip row with inline margins.

### 7.20 Timeline (restyle, keep markup)

Keep `<ol className="timeline">`. Put it inside a `.panel`. Each `<li>` has:

```tsx
<li>
  <div className="event">
    <time dateTime={...}>{...}</time>
    <span className="tag">{m.kind}</span>
    <span className="code">{m.qty} x {m.sku}</span>
  </div>
  <div className="detail">{from} {"->"} {to} ...</div>
</li>
```

The CSS draws a 1px vertical line and a hollow 9px dot per item.

### 7.21 Stat (slotting saving)

`<div className="stat">{value} <small>({pct}%)</small></div>`. Mono 28px weight 600; `small` is 16px `--text-secondary`.

### 7.22 Secret reveal (ActionForm)

Replace the inline styles in the secret block with `<div className="panel secret" role="alert">`. The `<code>` inside gets the `.secret-value` class (15px, breaks anywhere, `user-select: all`). Keep `translate="no"` and the texts.

---

## 8. Page specs

Every console page follows this skeleton:

```tsx
<>
  <PageHeader section="..." title="exact current h1 text" />
  <div className="page-body">
    {/* 1. toolbar (tabs, filters, page actions), if any */}
    {/* 2. Banner for load errors */}
    {/* 3. EmptyState or main content */}
  </div>
</>
```

Delete the old `<h1>` from each page. `PageHeader` renders it.

### 8.1 Console layout: `app/console/layout.tsx`

- Delete the `<header className="topbar">` block entirely. The warehouse switcher moves to `WarehouseSwitcher`, the user and sign out move to the sidebar footer.
- New structure:

```tsx
<div className="shell">
  <a className="skip" href="#main">Skip to content</a>
  <ConsoleNav warehouses={warehouses} wid={wid} user={session.user} role={session.role} />
  <main className="main" id="main">
    {wid ? children : <div className="page-body"><EmptyState>No warehouse exists yet. Run <code>crossdock generate</code> to create one.</EmptyState></div>}
  </main>
</div>
```

### 8.2 Stock: `console/stock/page.tsx`

- `PageHeader section="stock" title="Stock explorer"`.
- Toolbar: the filter form (`.filters`) with SKU and Location inputs and the Filter button (secondary, `Funnel` icon).
- Table columns: Location `location`, SKU `code`, Lot `code`, LPN `code`, Available `num`, Reserved `num`, In transit `num`, Quarantined `num`, Damaged `num`, (empty) `action`.
- Last cell: ghost small "History" link with the `ClockCounterClockwise` icon.
- A zero quantity renders in `--text-secondary`: add `className="num zero"` when the value is 0.
- Replace the "Next page" paragraph with `TableFoot` inside `.table-wrap`.

### 8.3 Tasks: `console/tasks/page.tsx`

- `PageHeader section="tasks" title="Task board"`.
- Replace `.cols` with the board (7.17). Column order stays: Exceptions, Open, Assigned, In progress.
- Column header: `StatusShape` for the column state, title, `badge` with the count (keep the "+" when there are more).
- Each task is a `details.card`. The `summary` shows the kind as a `.tag`, the `StatusChip`, and the main code.
- Card body: `dl.props` with icons (7.17). Keep all fields and their conditions.
- Assign form: `.row` with `<input className="input small" ...>` (delete the inline style) and `<SubmitButton className="btn small"><UserPlus size={14} aria-hidden="true" />Assign</SubmitButton>`. Import `UserPlus` from `@phosphor-icons/react/ssr`.

### 8.4 Orders and waves: `console/orders/page.tsx`

- `PageHeader section="orders" title="Orders and waves"`. Put the "Run allocation sweep" `ActionForm` in the `actions` prop. The button: `btn secondary small` with a `Play` icon.
- Toolbar: `Tabs label="Filter by state"` with the 9 filters. Labels unchanged ("all", "new", "partially allocated", ...).
- Table columns: (checkbox) `action`, Order `code`, Reference `code`, State `status`, Wave `code`, (empty) `action`.
- Last cell: ghost small "Timeline" link with `ClockCounterClockwise`.
- Release wave: move `<SubmitButton>Release wave for selected orders</SubmitButton>` into the `TableFoot` children slot, so the footer shows it on the left. Pass `count` and the next page link to `TableFoot` as usual. The `TableFoot` must stay inside the `ActionForm`, because the button submits it.
- Cancel an order: `<details className="panel narrow">`. Keep the summary text, the hint (as `<p className="muted">`), the field, and the danger button (add a `Prohibit` icon).

### 8.5 Receiving: `console/receiving/page.tsx`

- `PageHeader section="receiving" title="Receiving"`.
- Replace the inline-grid `.cols` with `.split`.
- Left: `h2` "Advance shipping notices", then the ASN table. Columns: Reference `code`, Supplier `party`, State `status`. The open ASN row gets `aria-current="true"`. `TableFoot` with the next page link.
- Right: `EmptyState` "Choose an ASN to see expected against received." when nothing is open. Otherwise a `.section-head` with `h2` (the ref, mono) and its `StatusChip`, then the lines table. Columns: SKU `code`, Expected `num`, Received `num`, Result `status`, (empty) `action`.
- "Close as short" stays `btn small secondary`.

### 8.6 Reconciliation: `console/reconciliation/page.tsx`

- `PageHeader section="reconciliation" title="Reconciliation workbench"`. Actions prop: the two `ActionForm`s "Schedule due counts" and "Reconcile receipts now", each `btn secondary small` with a `Play` icon.
- Ageing bar: keep as is (restyled by CSS). The intro paragraph becomes `<p className="page-intro">`.
- Toolbar: `Tabs label="Workbench sections"` with the two tabs, labels "Count variances" and "Inbound breaks". The counts move from the text "(N)" to the `count` prop: `count: wb.data ? sum(wb.data.variances) : undefined` (and `sum(wb.data.breaks)` for breaks).
- Variances table columns: Location `location`, SKU / lot `code`, System `num`, Counted `num`, Variance `num`, Proposed `status`, Age `age`, Decision `action`. Lot and LPN go in `<span className="sub">`. The flagged marker becomes `<StatusChip state="exception" label="review" />`.
- Breaks table columns: Break `status`, ASN / supplier `code`, SKU `code`, ASN `num`, Received `num`, Put away `num`, Age `age`, Resolve `action`. The kind becomes `<StatusChip state="exception" label={KIND_LABEL[...]} />` with the detail in `.sub` below.
- Decision forms: `.row` of `select.input.small`, `input.input.small`, and `btn small`. Keep names and ids.
- The next page link moves to `TableFoot`.

### 8.7 Settlement: `console/settlement/page.tsx`

- `PageHeader section="settlement" title="Settlement"`.
- Money position: `dl.kpis` with one `div.kpi` per value (7.15). Delete all inline styles.
- Toolbar: `Tabs label="Settlement sections"`.
- Cases tab: toolbar row with "Find unpaid shipments" (`btn secondary`, `Play` icon). Table columns: Case `status`, Shipment `code`, Expected `money`, Received `money`, Short by `money`, Age `age`, Resolve `action`. Case kind becomes `<StatusChip state="exception" label={KIND[c.kind] ?? c.kind} />` with the detail as `.sub`. Courier ref as `.sub` under the tracking link. "Short by" keeps `<strong>`.
- Shipments tab: `Tabs label="Filter by status"` in the toolbar. Columns: Tracking `code`, Courier `party`, COD `money`, Status `status`, Dispatched `date`. Delete the `fontSize: 14` inline styles. Open case count stays as muted text after the chip. `TableFoot` with "Older shipments".
- Remittances tab: upload form in `section.panel.narrow`. The CSV hint becomes `<p className="muted">`. The upload button keeps its text "Upload and match" and gets an `UploadSimple` icon. Table columns: File `code`, Courier `party`, Received `date`, Lines `num`, Matched `num`, Need attention `num`.
- Terms tab: terms table columns: Courier `party`, COD fee `money`, Shipping cost `money`, Pays within `age`. Form in `section.panel.narrow`.

### 8.8 Returns: `console/returns/page.tsx`

- `PageHeader section="returns" title="Returns"`.
- Toolbar: `Tabs label="Filter by state"`.
- Table columns: Return `code`, Tracking `code`, Reason `text`, State `status`, Units `code`, Decision `text`. The reason renders as a `.tag`. Delete the `fontSize: 14` styles. Each unit line and decision line is a `<div>` (keep), now inheriting 13px mono from the cell.
- `TableFoot` with "Older returns".

### 8.9 Slotting: `console/slotting/page.tsx`

- `PageHeader section="slotting" title="Slotting"`. The intro becomes `p.page-intro`.
- `.split`. Left: `h2` "Proposals", then `ul.list` of `li > Link.list-item` with `aria-current` on the selected one. Inside: a `.row` with the `StatusChip` and the id (`.code`), then `.sub` with date, moves, and solver. Delete `.scan-list` and the panel-inside-link markup.
- Right: `h2` "Proposal" plus the id. A `.panel` for the replayed saving. The big number uses `.stat`. Delete all inline sizes.
- "What the floor did" panel: `.panel` with `role="status"`.
- Accept form: `btn` with a `Check` icon (text unchanged). The disabled hint becomes `.hint`.
- Reject form: `.row` with `input.input` (delete the inline style) and `btn danger` with a `Prohibit` icon.
- `h3` "Moves (fastest movers first)" with a ghost small link "show on the floor map" and the `MapTrifold` icon.
- Moves table columns: # `num`, SKU `code`, From `location`, To `location`, Why `text`.

### 8.10 Floor map: `console/map/page.tsx`

- `PageHeader section="map" title="Floor map"`. Intro as `p.page-intro`.
- Toolbar: `Tabs label="Period"` for the day options. "Hide proposal" becomes a `btn secondary small` after a `.spacer`.
- The map: `<div className="panel map-wrap">` and `<svg className="map-svg" ...>`. Keep `role="img"` and the `aria-label`.
- Move markers: replace the inline `style` on the `rect` with classes. Moving in: `className="heat hN move-in"`. Moving out: `className="heat hN move-out"`. Bulk cells: `className="heat hN bulk"` (instead of the `strokeDasharray` attribute).
- Legend: `<ul className="legend" aria-label="Legend">` with `<li>` items. Delete inline styles. Legend swatches use the same classes.
- `h2` "Busiest locations", then a `.table-wrap.narrow` table. Columns: Location `location`, Zone `location`, Picks `num`. Zone renders as a `.tag`.

### 8.11 Timeline: `console/timeline/page.tsx`

- `PageHeader section="timeline" title="Movement timeline"`.
- Toolbar: the filter form (four inputs, Filter button with `Funnel`). The labels now come from the key (`k.replace("_", " ")`), which gives "sku" and "order id". Replace that with a fixed map: `{ sku: "SKU", location: "Location", order_id: "Order id", task_id: "Task id" }`.
- `<div className="panel"><ol className="timeline">` with the item markup from 7.20. Keep the class `timeline`.
- "Later movements" becomes a `btn secondary small` with a trailing `CaretRight`, in a `.toolbar` after the panel.

### 8.12 Webhooks list: `console/endpoints/page.tsx`

- `PageHeader section="endpoints" title="Webhook endpoints"`.
- Table columns: Endpoint `code`, URL `link`, Events `code`, Breaker `status`, Pending `num`, Delivered `num`, Dead letters `num`. URL cell: `td.code.break`. Events render as one `.tag.code` per event.
- "Add an endpoint": `section.panel.narrow`. Delete inline styles. The events hint becomes `<span className="hint">`.

### 8.13 Webhook detail: `console/endpoints/[id]/page.tsx`

- `PageHeader section="endpoints" title={e.id} parent code`. Delete the old `h1`.
- `.record-meta`: the two `StatusChip`s, then the URL in `span.code.break`.
- The dead-letter warning: `<Banner role="status">` (keep the text).
- `.cols` with the Replay panel and the Admin panel. Replace both inline `<hr>` with `<hr className="divider" />`. Buttons: replay buttons `btn secondary` with `Play`; "Rotate secret" `btn secondary` with `Key`; "Disable endpoint" `btn danger` with `Prohibit`.
- `h2` "Deliveries", `Tabs label="Filter by state"`, table columns: Event `code`, Topic `code`, State `status`, Attempts `num`, Last status `num`, Detail `text`. The attempts list becomes `<ol className="attempts">`. `TableFoot` with "Older deliveries".

### 8.14 Shipment: `console/shipments/[id]/page.tsx`

- `PageHeader section="settlement" title="One shipment, start to finish" parent`.
- The id line becomes `<p className="record-meta code">{id}</p>`.
- Timeline inside `.panel` as in 8.11. Event kind labels use `.tag`.

---

## 9. Scanner: `app/scan/ScannerApp.tsx`

The scanner keeps its structure, behaviour, and large sizes. It adopts the new tokens, faces, radii, and icons. Read section 0 again: the scanner e2e tests are strict.

### 9.1 Frame

- `.scan`: full height, flex column, `--bg-subtle` backdrop, body 18px.
- `.scan-head`: `--bg`, bottom border, 12px 16px padding, flex column with 2px gap. Its first `.code` is 28px.
- `.scan-body`: 16px padding, flex column, 16px gap.
- `.scan-bar` (sync bar): `--bg`, top border, 14px weight 600. Keep its four children and their texts. The first `<span>` gets `className="status"` and keeps the `WifiHigh` / `WifiSlash` icon (change its size from 20 to 18). The "Sync now" button becomes `btn secondary small icon` (keep its `aria-label`).
- `.scan-actions`: `--bg`, top border, safe-area bottom padding.

### 9.2 Buttons

Inside `.scan`, every `.btn` is 56px tall, 18px weight 600, 12px radius. `.btn.small` is 48px, 16px. Delete every inline `minHeight: 56` and `flex: 1`.

- Home screen actions: `<div className="scan-actions scan-grid">`. The three secondary buttons sit in a 3-column grid. The primary "Get next task" spans the full row (`.btn.primary` inside `.scan-grid` spans all columns). Icons: Receive `Truck`, Return `ArrowUUpLeft`, Ship `TruckTrailer` (before text, 22, bold); Get next task `ArrowRight` after the text.
- Back buttons: `className="btn text back"` with an `ArrowLeft` icon (22, bold) before the text. Delete the inline colour. The accessible name stays "Back".
- Exception text buttons (short, damaged, skip): `className="btn text"` (stop colour).
- On the ASN list and delivery screens, the Back buttons are `btn secondary` with the `ArrowLeft` icon and a `.grow` class (`flex: 1`).
- Return intake condition buttons (Good, Damaged, Not sure): wrap in `<div className="scan-choice">` (3-column grid). Delete inline styles. "Good" stays `.btn`, the others `.btn secondary`.

### 9.3 Lists and cards

- `.scan-list button`: white card, 1px `--border-strong`, 12px radius, 64px minimum height, `--shadow-xs`, 12px 16px padding.
- Task rows: kind in `<span className="tag">` (14px inside the scanner), code `.code`, SKU line `div.code.code-s`.
- Disposition options: the suggested option gets `data-suggested="true"` instead of the inline border style. Option label: `<strong className="option-title">` (22px). Hint: `div.muted`.
- Count rows: `li.panel`, SKU `div.code.code-l`, lot line `div.sub.code`, inputs `input.scan-input.qty` and `input.scan-input.qty.qty-sm`. Delete inline styles. Keep the ids `q-0`, `d-0`, and so on.
- Return lines: `li.panel.scan-line` (flex row). Delete the inline margin on "Remove".
- Step list: `<ol className="steps">`. A not-done step shows `<span className="todo-dot" aria-label="to do" />` instead of the inline-styled span.

### 9.4 Codes and text in the scanner

| Current inline style | Replacement |
|---|---|
| `className="code" style={{ fontSize: 16 }}` | `className="code code-s"` |
| `className="code" style={{ fontSize: 18 }}` | `className="code code-m"` |
| `className="code" style={{ fontSize: 20 }}` | `className="code code-l"` (24px; one size step, accepted) |
| `className="code" style={{ fontSize: 24 }}` | `className="code code-l"` |
| `<span style={{ fontSize: 16 }}>` (instruction text in head) | `<span className="head-note">` |
| `className="muted" style={{ fontSize: 14, fontFamily: ... }}` | `className="sub code"` |

### 9.5 Feedback

Keep the screen-edge flash, `.scan-last`, sound, and haptics as they are. `.scan-last` uses `--ok` and `--stop`.

### 9.6 Manifest and theme colour

- `public/manifest.webmanifest`: `"background_color": "#111113"`, `"theme_color": "#2563EB"`.
- `app/layout.tsx` `viewport.themeColor`: `[{ media: "(prefers-color-scheme: light)", color: "#FFFFFF" }, { media: "(prefers-color-scheme: dark)", color: "#161618" }]`.
- Do not change `public/icon.svg`.

---

## 10. Login: `app/login/page.tsx`

```tsx
<main className="auth">
  <div className="auth-card">
    <span className="brand-mark brand-mark-lg"><Warehouse size={18} weight="bold" aria-hidden="true" /></span>
    <h1>Sign in to Crossdock</h1>
    <p className="muted">Warehouse console and scanner.</p>
    <form method="post" action="/api/login" className="form">
      {error && <Banner>{...existing messages...}</Banner>}
      {fields unchanged}
      <button className="btn block" type="submit">Sign in</button>
    </form>
  </div>
</main>
```

- Import `Warehouse` from `@phosphor-icons/react/ssr`.
- Centred card, 380px wide, on `--bg-subtle`. The button is full width and 36px tall.
- Keep the input ids, names, `autoComplete`, `autoFocus`, and the error texts.

---

## 11. Inline style replacement map

After the revamp, `grep -rn "style={{" web/src` returns only SVG geometry in `map/page.tsx` (none, if the move markers use classes) and nothing else. Use this table.

| File | Current inline style | Replacement |
|---|---|---|
| `components/ActionForm.tsx` | secret panel `marginTop: 8` | `.panel.secret` |
| `components/ActionForm.tsx` | code `fontSize: 15, wordBreak, userSelect` | `.secret-value` |
| `components/ActionForm.tsx` | p `margin: "8px 0 0"` | delete (panel gap) |
| `console/layout.tsx` | select `minHeight: 40` | deleted with the top bar |
| all pages | `style={{ marginBottom: 12 }}` / `16` on rows and navs | delete; use `.toolbar` or rely on `.page-body` gap |
| all pages | `style={{ marginTop: 0 }}` on intro `p` | `.page-intro` |
| all pages | `style={{ marginTop: 16 }}` / `20` on panels | delete (`.page-body` gap) |
| all pages | `maxWidth: 560` / `520` | `.narrow` |
| all pages | `style={{ fontSize: 13 }}` on muted spans in cells | `.sub` |
| all pages | `style={{ fontSize: 14 }}` on cells | delete (body is 14px, codes 13px) |
| all pages | `wordBreak: "break-all"` | `.break` |
| `receiving`, `slotting` | grid template on `.cols` | `.split` |
| `settlement` | `dl.cols` money cards and their inner styles | `.kpis` / `.kpi` |
| `slotting` | proposal list panels and inline borders | `.list` / `.list-item` with `aria-current` |
| `slotting` | `fontSize: 32` / `20` saving | `.stat` with `small` |
| `slotting` | `fontSize: 13, margin` footnote | `.hint` |
| `slotting`, `tasks` | input inline borders and sizes | `.input` / `.input.small` |
| `slotting` | `h3 style={{ fontSize: 15 }}` and link `fontSize: 14` | plain `h3`, `btn ghost small` link |
| `tasks` | `details` border and padding | `details.card` |
| `tasks` | `dl` margin | `dl.props` |
| `orders` | checkbox `width: 24, height: 24` | delete (CSS sizes checkboxes) |
| `endpoints/[id]` | `hr` inline border | `hr.divider` |
| `endpoints/[id]` | `ol` attempts inline | `ol.attempts` |
| `endpoints/[id]` | `h1 fontSize: 20` | deleted; `PageHeader code` |
| `map` | panel `overflow, padding` | `.map-wrap` |
| `map` | svg `display, maxWidth` | `.map-svg` |
| `map` | legend `ul` and `li` gaps | `.legend` |
| `map` | `rect` stroke styles for moves | `.move-in` / `.move-out` |
| `shipments/[id]` | `p fontSize: 14` | `.record-meta.code` |
| `ScannerApp` | see section 9 | see section 9 |

---

## 12. Implementation order

Do the phases in order. Run the section 13 checks at the end of each phase. Commit after each phase.

**Phase 1: foundation**
1. `app/layout.tsx`: replace Atkinson with Inter (`Inter({ subsets: ["latin"], variable: "--font-inter", display: "swap" })`). JetBrains Mono weights `["400", "500", "600"]`. Update `viewport.themeColor` (9.6).
2. `app/globals.css`: replace the whole file with Appendix A.
3. Replace old token names in TSX (3.5).

**Phase 2: shared components**
4. Create `lib/nav.ts`, `components/PageHeader.tsx`, `components/WarehouseSwitcher.tsx`, `components/Tabs.tsx`, `components/Table.tsx`, `components/Banner.tsx`, `components/EmptyState.tsx`.
5. Update `components/StatusChip.tsx` (7.9), `components/ActionForm.tsx` (Banner, secret block), `components/SignOutButton.tsx`.
   - `SignOutButton` gets an optional `iconOnly` prop. With `iconOnly`, it renders only the `SignOut` icon (16) and `aria-label="Sign out"`. Without it, it renders the icon and the text "Sign out". The scanner uses the text version.
6. Rewrite `components/ConsoleNav.tsx` (7.2).

**Phase 3: shell**
7. `app/console/layout.tsx` (8.1).

**Phase 4: console pages**, one commit per page or per two pages
8. Stock, Tasks, Orders, Receiving, Reconciliation, Settlement, Returns, Slotting, Floor map, Timeline, Webhooks, Webhook detail, Shipment (8.2 to 8.14).

**Phase 5: login** (section 10)

**Phase 6: scanner** (section 9), and the manifest.

**Phase 7: docs**
9. `CROSSDOCK_SPEC.md` section 12 already matches this document. Do not edit it.
10. If the docs book (`docs/`) has a design page, add one line there: "Visual values are defined in DESIGN.md."

---

## 13. Verification

Run these from `web/` after every phase. All must pass before the next phase.

```bash
npm run typecheck
```

```bash
npm test
```

After phases 4 and 6, also run the e2e suite (it needs the API and database running; see the README):

```bash
npx playwright test
```

Text checks (run from the repo root). Each must print nothing:

```bash
grep -rn "\-\-ink\|\-\-line\|\-\-action\|\-\-surface" web/src
```

```bash
grep -rn "style={{" web/src
```

```bash
grep -rn "banner-error\|className=\"empty\"\|className=\"topbar\"\|className=\"rail\"" web/src --include=*.tsx
```

```bash
grep -rn "text-transform: uppercase\|letter-spacing: 0.08em" web/src/app/globals.css
```

Emoji check (prints the files that contain emoji; must print nothing):

```bash
node -e "const fs=require('fs'),p=require('path');const w=d=>fs.readdirSync(d,{withFileTypes:true}).flatMap(e=>e.isDirectory()?w(p.join(d,e.name)):[p.join(d,e.name)]);for(const f of w('web/src'))if(/\p{Extended_Pictographic}/u.test(fs.readFileSync(f,'utf8')))console.log(f)"
```

Visual checks (run `npm run dev` and open each page):

1. Every console page has the 48px page bar with an icon tile and a title. No page has a second `<h1>`.
2. The sidebar shows the warehouse switcher, three titled groups, the Scanner link, and the user row. The current page is highlighted. Changing the warehouse select reloads with the new warehouse.
3. Every table has type icons in its header, hairlines between columns, no zebra rows, a hover row tint, and a footer with the row count.
4. Every status is a tinted pill with a shape. No bare coloured text is used for status.
5. Only one primary (blue) button per view.
6. Inter renders: the lower-case `l` has a tail and the upper-case `I` has serifs. If not, the `cv05` / `cv08` features are not active; check `font-feature-settings` on `body`.
7. Dark mode (set the OS to dark, or add `data-theme="dark"` on `<html>` in dev tools): every page is readable, no white boxes remain, and borders are visible.
8. At 390px width: the sidebar is a horizontal strip, split views stack, and there is no horizontal page scroll (tables scroll inside `.table-wrap`).
9. Scanner at 390px: all buttons are at least 48px tall, the primary button is 56px, the sync bar shows the cloud icon and "Online" or "Offline".
10. Keyboard: Tab through the sidebar, tabs, filters, table actions, and forms. Every focused element shows the accent focus ring.

---

## 14. Do not

- Do not use emoji. Use Phosphor icons.
- Do not colour icons for decoration. Icons are `currentColor`.
- Do not colour tags. Colour means status.
- Do not add new features: no search, no command palette, no avatars, no theme toggle, no charts.
- Do not use uppercase labels or letter-spaced labels.
- Do not use more than one primary button in a view.
- Do not add shadows, gradients, blur, or animation beyond Appendix A.
- Do not add npm packages.
- Do not change text that the e2e tests read (section 0, rule 3).
- Do not use `--text-placeholder` for real content.
- Do not put raw hex colours in TSX.

---

## Appendix A: `web/src/app/globals.css`

Replace the whole file with this.

```css
/* Crossdock design system. Spec: DESIGN.md. Status colours are for status only. */
:root {
  --bg: #ffffff;
  --bg-subtle: #fafafa;
  --bg-hover: #f4f4f5;
  --bg-active: #efeff1;
  --border: #e9e9ec;
  --border-strong: #d6d7db;
  --text: #1c1d1f;
  --text-secondary: #505259;
  --text-placeholder: #8a8c93;
  --accent: #2563eb;
  --accent-hover: #1d4ed8;
  --accent-soft: #eff4ff;
  --on-accent: #ffffff;
  --ok: #166534; --ok-bg: #ecfdf3;
  --attention: #854d0e; --attention-bg: #fef7e0;
  --stop: #991b1b; --stop-bg: #fef2f2;
  --hold: #5b21b6; --hold-bg: #f5f3ff;
  --pending: #505259; --pending-bg: #f4f4f5;
  --shadow-xs: 0 1px 2px rgba(16, 17, 19, 0.06);
  --shadow-sm: 0 1px 3px rgba(16, 17, 19, 0.08), 0 1px 2px rgba(16, 17, 19, 0.04);
  --r-sm: 4px; --r-md: 6px; --r-lg: 8px; --r-xl: 12px;
  --font-ui: var(--font-inter), system-ui, sans-serif;
  --font-code: var(--font-jetbrains), ui-monospace, monospace;
  color-scheme: light dark;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --bg: #161618; --bg-subtle: #111113; --bg-hover: #232327; --bg-active: #2a2a2e;
    --border: #2a2a2e; --border-strong: #3a3a40;
    --text: #ededef; --text-secondary: #a1a3aa; --text-placeholder: #6e7079;
    --accent: #6ea8ff; --accent-hover: #8fbcff; --accent-soft: #172036; --on-accent: #0b0b0c;
    --ok: #4ade80; --ok-bg: #16271d; --attention: #facc15; --attention-bg: #2a2410;
    --stop: #fca5a5; --stop-bg: #2d1717; --hold: #c4b5fd; --hold-bg: #221b33;
    --pending: #a1a3aa; --pending-bg: #232327;
    --shadow-xs: none; --shadow-sm: 0 0 0 1px var(--border);
  }
}
:root[data-theme="dark"] {
  --bg: #161618; --bg-subtle: #111113; --bg-hover: #232327; --bg-active: #2a2a2e;
  --border: #2a2a2e; --border-strong: #3a3a40;
  --text: #ededef; --text-secondary: #a1a3aa; --text-placeholder: #6e7079;
  --accent: #6ea8ff; --accent-hover: #8fbcff; --accent-soft: #172036; --on-accent: #0b0b0c;
  --ok: #4ade80; --ok-bg: #16271d; --attention: #facc15; --attention-bg: #2a2410;
  --stop: #fca5a5; --stop-bg: #2d1717; --hold: #c4b5fd; --hold-bg: #221b33;
  --pending: #a1a3aa; --pending-bg: #232327;
  --shadow-xs: none; --shadow-sm: 0 0 0 1px var(--border);
}

/* Base */
*, *::before, *::after { box-sizing: border-box; }
html { background: var(--bg); }
body {
  margin: 0; background: var(--bg); color: var(--text);
  font: 400 14px/20px var(--font-ui);
  font-feature-settings: "cv05" 1, "cv08" 1;
  -webkit-font-smoothing: antialiased;
}
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
a, button, input, select, summary, label { touch-action: manipulation; -webkit-tap-highlight-color: transparent; }
h1, h2, h3 { text-wrap: balance; }
h1 { font-size: 15px; line-height: 20px; font-weight: 600; margin: 0; }
h2 { font-size: 14px; line-height: 20px; font-weight: 600; margin: 0; }
h3 { font-size: 13px; line-height: 18px; font-weight: 600; margin: 0; color: var(--text-secondary); display: flex; align-items: center; gap: 8px; }
p { margin: 0; }

/* Text */
.code, code, .num { font-family: var(--font-code); font-variant-numeric: tabular-nums slashed-zero; font-feature-settings: "zero" 1; }
.code, code { font-size: 13px; font-weight: 500; }
.num { font-size: 13px; text-align: right; }
.num.zero { color: var(--text-secondary); }
.label { font-size: 13px; line-height: 18px; font-weight: 500; color: var(--text-secondary); }
.muted { color: var(--text-secondary); }
.sub { display: block; font-size: 12px; line-height: 16px; font-weight: 400; color: var(--text-secondary); }
.hint { font-size: 12px; line-height: 16px; color: var(--text-secondary); }
.page-intro { color: var(--text-secondary); max-width: 72ch; }
.break { overflow-wrap: anywhere; word-break: break-all; }
.stat { font: 600 28px/36px var(--font-code); font-variant-numeric: tabular-nums slashed-zero; }
.stat small { font-size: 16px; font-weight: 500; color: var(--text-secondary); }

/* Shell */
.shell { display: grid; grid-template-columns: 240px minmax(0, 1fr); min-height: 100dvh; }
.sidebar {
  position: sticky; top: 0; height: 100dvh; overflow-y: auto;
  display: flex; flex-direction: column; gap: 2px; padding: 8px;
  background: var(--bg-subtle); border-right: 1px solid var(--border);
}
.nav-group { display: flex; flex-direction: column; gap: 2px; margin-top: 16px; }
.nav-group-title { height: 24px; padding: 0 8px; display: flex; align-items: center; font-size: 12px; font-weight: 500; color: var(--text-secondary); }
.nav-item {
  display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 8px;
  border-radius: var(--r-md); color: var(--text); font-weight: 500; text-decoration: none;
  transition: background-color 120ms;
}
.nav-item svg { flex: none; color: var(--text-secondary); }
.nav-item:hover { background: var(--bg-hover); text-decoration: none; }
.nav-item[aria-current="page"] { background: var(--bg-active); }
.nav-item[aria-current="page"] svg { color: var(--text); }
.sidebar-foot { margin-top: auto; padding-top: 8px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 2px; }
.user-row { display: flex; align-items: center; gap: 8px; height: 40px; padding: 0 4px 0 8px; color: var(--text-secondary); }
.user-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); font-weight: 500; }

/* Warehouse switcher */
.ws { position: relative; display: flex; align-items: center; gap: 8px; height: 44px; padding: 0 8px; border-radius: var(--r-lg); transition: background-color 120ms; }
.ws:hover { background: var(--bg-hover); }
.ws:focus-within { outline: 2px solid var(--accent); outline-offset: 0; }
.ws select { position: absolute; inset: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; }
.ws-text { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.ws-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-id { font-size: 12px; line-height: 16px; font-weight: 400; color: var(--text-secondary); }
.ws-caret { flex: none; color: var(--text-secondary); }
.brand-mark { width: 24px; height: 24px; display: inline-grid; place-items: center; flex: none; border-radius: var(--r-md); background: var(--text); color: var(--bg); }
.brand-mark-lg { width: 36px; height: 36px; border-radius: var(--r-lg); }

/* Page */
.main { min-width: 0; display: flex; flex-direction: column; }
.page-head {
  position: sticky; top: 0; z-index: 10; height: 48px; flex: none;
  display: flex; align-items: center; gap: 8px; padding: 0 20px;
  background: var(--bg); border-bottom: 1px solid var(--border);
}
.page-head h1.code { font-size: 15px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.page-head .actions { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.icon-tile { width: 24px; height: 24px; display: inline-grid; place-items: center; flex: none; border: 1px solid var(--border); border-radius: var(--r-md); background: var(--bg); color: var(--text-secondary); box-shadow: var(--shadow-xs); }
.crumb { color: var(--text-secondary); font-weight: 500; }
.crumb-sep { color: var(--text-placeholder); flex: none; }
.page-body { display: flex; flex-direction: column; gap: 16px; padding: 16px 20px 32px; min-width: 0; }
.toolbar { display: flex; align-items: flex-end; gap: 8px; flex-wrap: wrap; }
.toolbar .spacer, .spacer { flex: 1; }
.section-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.record-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; color: var(--text-secondary); font-size: 13px; }

/* Layout helpers */
.row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; align-items: start; }
.split { display: grid; grid-template-columns: minmax(280px, 360px) minmax(0, 1fr); gap: 16px; align-items: start; }
.split > section { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.narrow { max-width: 560px; }
.divider { border: 0; border-top: 1px solid var(--border); margin: 4px 0; width: 100%; }

/* Panels and cards */
.panel { display: flex; flex-direction: column; gap: 12px; padding: 16px; background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-xl); box-shadow: var(--shadow-xs); }
details.panel > summary { cursor: pointer; font-weight: 600; min-height: 24px; }
.kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; margin: 0; }
.kpi { padding: 12px 16px; background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-xl); box-shadow: var(--shadow-xs); }
.kpi dt { font-size: 13px; font-weight: 500; color: var(--text-secondary); }
.kpi dd { margin: 4px 0 0; font: 600 22px/28px var(--font-code); font-variant-numeric: tabular-nums slashed-zero; }
.kpi .sub { margin-top: 2px; }
.empty { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 40px 16px; text-align: center; color: var(--text-secondary); background: var(--bg-subtle); border: 1px dashed var(--border-strong); border-radius: var(--r-xl); }
.empty svg { color: var(--text-placeholder); }
.empty p { max-width: 48ch; }
.secret-value { font-size: 15px; overflow-wrap: anywhere; word-break: break-all; user-select: all; }

/* Tables */
.table-wrap { overflow: auto; max-height: calc(100dvh - 200px); background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-lg); }
table { width: 100%; border-collapse: separate; border-spacing: 0; }
th {
  position: sticky; top: 0; z-index: 1; height: 36px; padding: 0 12px; text-align: left; white-space: nowrap;
  font-size: 13px; font-weight: 500; color: var(--text-secondary);
  background: var(--bg-subtle); border-bottom: 1px solid var(--border);
}
th .th-in { display: inline-flex; align-items: center; gap: 6px; }
td { height: 40px; padding: 6px 12px; vertical-align: middle; border-bottom: 1px solid var(--border); }
th + th, td + td { border-left: 1px solid var(--border); }
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover td { background: var(--bg-hover); }
tbody tr[aria-current="true"] td { background: var(--accent-soft); }
th.num, td.num { text-align: right; }
th.check, td.check { width: 44px; text-align: center; }
input[type="checkbox"] { width: 16px; height: 16px; margin: 0; accent-color: var(--accent); vertical-align: middle; }
.table-foot {
  position: sticky; bottom: 0; left: 0; display: flex; align-items: center; justify-content: space-between; gap: 12px;
  min-height: 44px; padding: 6px 12px; font-size: 13px; color: var(--text-secondary);
  background: var(--bg-subtle); border-top: 1px solid var(--border);
}
.attempts { margin: 4px 0 0; padding-left: 20px; font: 400 12px/18px var(--font-code); }

/* Buttons */
.btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 6px; height: 32px; padding: 0 12px;
  font: 500 14px/1 var(--font-ui); white-space: nowrap; text-decoration: none; cursor: pointer;
  color: var(--on-accent); background: var(--accent); border: 1px solid var(--accent); border-radius: var(--r-lg);
  box-shadow: var(--shadow-xs); transition: background-color 120ms, border-color 120ms, color 120ms;
}
.btn svg { flex: none; }
.btn:hover:not([disabled]) { background: var(--accent-hover); border-color: var(--accent-hover); text-decoration: none; }
.btn.secondary { color: var(--text); background: var(--bg); border-color: var(--border-strong); }
.btn.secondary:hover:not([disabled]) { background: var(--bg-hover); border-color: var(--border-strong); }
.btn.danger { color: var(--stop); background: var(--bg); border-color: var(--border-strong); }
.btn.danger:hover:not([disabled]) { background: var(--stop-bg); border-color: var(--stop); }
.btn.ghost { color: var(--text-secondary); background: transparent; border-color: transparent; box-shadow: none; }
.btn.ghost:hover:not([disabled]) { color: var(--text); background: var(--bg-hover); border-color: transparent; }
.btn.small { height: 28px; padding: 0 10px; font-size: 13px; border-radius: var(--r-md); }
.btn.icon { width: 32px; padding: 0; }
.btn.small.icon { width: 28px; }
.btn.block { width: 100%; height: 36px; }
.btn[disabled] { opacity: 0.5; cursor: not-allowed; }
.skip { position: absolute; left: 8px; top: -60px; z-index: 110; padding: 10px 14px; color: var(--on-accent); background: var(--accent); border-radius: var(--r-lg); }
.skip:focus { top: 8px; }

/* Tabs, tags, badges */
.tabs { display: inline-flex; flex-wrap: wrap; gap: 2px; padding: 2px; background: var(--bg-subtle); border: 1px solid var(--border); border-radius: var(--r-lg); }
.tabs a {
  display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 10px; white-space: nowrap;
  font-size: 13px; font-weight: 500; color: var(--text-secondary); text-decoration: none; border-radius: var(--r-md);
  transition: background-color 120ms, color 120ms;
}
.tabs a:hover { color: var(--text); text-decoration: none; }
.tabs a[aria-current="page"] { color: var(--text); background: var(--bg); box-shadow: var(--shadow-sm); }
.tag { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 6px; font-size: 12px; font-weight: 500; line-height: 1; white-space: nowrap; color: var(--text); background: var(--bg-hover); border: 1px solid var(--border); border-radius: var(--r-sm); }
.tag.code { font-family: var(--font-code); }
.badge { display: inline-block; min-width: 20px; height: 20px; padding: 0 6px; text-align: center; font: 500 12px/20px var(--font-code); color: var(--text-secondary); background: var(--bg-hover); border-radius: var(--r-sm); }

/* Status chip: shape + colour + label, never colour alone */
.chip { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 8px; font-size: 12px; font-weight: 500; line-height: 1; white-space: nowrap; color: var(--pending); background: var(--pending-bg); border-radius: var(--r-md); }
.chip svg, .shape { flex: none; }
.chip[data-tone="ok"], .shape[data-tone="ok"] { color: var(--ok); }
.chip[data-tone="attention"], .shape[data-tone="attention"] { color: var(--attention); }
.chip[data-tone="stop"], .shape[data-tone="stop"] { color: var(--stop); }
.chip[data-tone="hold"], .shape[data-tone="hold"] { color: var(--hold); }
.shape[data-tone="pending"] { color: var(--pending); }
.chip[data-tone="ok"] { background: var(--ok-bg); }
.chip[data-tone="attention"] { background: var(--attention-bg); }
.chip[data-tone="stop"] { background: var(--stop-bg); }
.chip[data-tone="hold"] { background: var(--hold-bg); }

/* Banners */
.banner { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; font-size: 14px; border: 1px solid; border-radius: var(--r-lg); }
.banner svg { flex: none; margin-top: 2px; }
.banner-error { color: var(--stop); background: var(--stop-bg); border-color: color-mix(in srgb, var(--stop) 30%, transparent); }
.banner-ok { color: var(--ok); background: var(--ok-bg); border-color: color-mix(in srgb, var(--ok) 30%, transparent); }
.banner-info { color: var(--text); background: var(--bg-subtle); border-color: var(--border); }

/* Forms */
.form { display: flex; flex-direction: column; }
.field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; }
.field .error { color: var(--stop); font-size: 13px; font-weight: 500; }
.filters { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 8px; }
.filters .field { margin-bottom: 0; }
.input, .field input, .field select, .field textarea, .filters input, .filters select {
  height: 32px; min-width: 0; padding: 0 10px; font: 400 14px var(--font-ui); color: var(--text);
  background: var(--bg); border: 1px solid var(--border-strong); border-radius: var(--r-lg); box-shadow: var(--shadow-xs);
  transition: border-color 120ms, box-shadow 120ms;
}
.field input[type="file"] { height: auto; padding: 6px; }
.input::placeholder, .field input::placeholder, .filters input::placeholder { color: var(--text-placeholder); }
.input:focus, .field input:focus, .field select:focus, .field textarea:focus, .filters input:focus, .filters select:focus {
  outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 20%, transparent);
}
.input.code, .field input.code, .filters input.code { font-family: var(--font-code); font-size: 13px; }
.input.small { height: 28px; padding: 0 8px; font-size: 13px; border-radius: var(--r-md); }

/* Board */
.board { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(280px, 1fr); gap: 12px; align-items: start; overflow-x: auto; padding-bottom: 4px; }
.board-col { display: flex; flex-direction: column; gap: 8px; min-height: 120px; padding: 8px; background: var(--bg-subtle); border: 1px solid var(--border); border-radius: var(--r-xl); }
.board-col-head { display: flex; align-items: center; gap: 8px; height: 28px; padding: 0 4px; }
.board-col-head h2 { font-size: 13px; }
.board-empty { padding: 8px 4px; font-size: 13px; color: var(--text-secondary); }
.card { background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-lg); box-shadow: var(--shadow-xs); }
.card > summary { display: flex; flex-direction: column; gap: 6px; padding: 10px 12px; list-style: none; cursor: pointer; }
.card > summary::-webkit-details-marker { display: none; }
.card[open] > summary { border-bottom: 1px solid var(--border); }
.card-body { display: flex; flex-direction: column; gap: 10px; padding: 10px 12px; }
.props { display: grid; grid-template-columns: max-content 1fr; gap: 6px 12px; margin: 0; font-size: 13px; }
.props dt { display: flex; align-items: center; gap: 6px; color: var(--text-secondary); }
.props dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }

/* List (split views) */
.list { list-style: none; margin: 0; padding: 0; overflow: hidden; background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-xl); }
.list > li + li { border-top: 1px solid var(--border); }
.list-item { display: flex; flex-direction: column; gap: 4px; padding: 10px 12px; color: var(--text); text-decoration: none; transition: background-color 120ms; }
.list-item:hover { background: var(--bg-hover); text-decoration: none; }
.list-item[aria-current="true"] { background: var(--accent-soft); box-shadow: inset 2px 0 0 var(--accent); }

/* Timeline */
.timeline { list-style: none; margin: 0; padding: 0; }
.timeline li { position: relative; padding: 0 0 16px 24px; }
.timeline li::before { content: ""; position: absolute; left: 7px; top: 16px; bottom: -4px; width: 1px; background: var(--border); }
.timeline li:last-child { padding-bottom: 0; }
.timeline li:last-child::before { display: none; }
.timeline li::after { content: ""; position: absolute; left: 2px; top: 5px; width: 11px; height: 11px; border-radius: 50%; background: var(--bg); border: 2px solid var(--text-secondary); }
.timeline .event { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.timeline time { font: 400 12px var(--font-code); color: var(--text-secondary); }
.timeline .detail { margin-top: 2px; font: 400 12px/18px var(--font-code); color: var(--text-secondary); }

/* Ageing bar: number + label + bar pattern, never colour alone */
.ageing { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.ageing > div { position: relative; overflow: hidden; padding: 12px 16px 12px 20px; background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-xl); box-shadow: var(--shadow-xs); }
.ageing > div::before { content: ""; position: absolute; left: 0; top: 0; bottom: 0; width: 4px; background: currentColor; }
.ageing .n { display: block; font: 600 22px/28px var(--font-code); font-variant-numeric: tabular-nums slashed-zero; color: var(--text); }
.ageing .lbl { font-size: 13px; font-weight: 500; color: var(--text-secondary); }
.ageing .b0 { color: var(--ok); }
.ageing .b1 { color: var(--attention); }
.ageing .b2 { color: var(--attention); }
.ageing .b2::before { width: 8px; background: repeating-linear-gradient(135deg, currentColor 0 3px, transparent 3px 6px); }
.ageing .b3 { color: var(--stop); }
.ageing .b3::before { width: 6px; }

/* Floor map: heat is lightness steps of the accent, plus the pick count in the cell and a dashed edge for bulk */
.map-wrap { overflow: auto; padding: 8px; }
.map-svg { display: block; max-width: 100%; height: auto; }
.heat { fill: var(--bg); stroke: var(--border-strong); stroke-width: 1.5; }
.heat.h1 { fill: color-mix(in srgb, var(--accent) 18%, var(--bg)); stroke: var(--accent); }
.heat.h2 { fill: color-mix(in srgb, var(--accent) 40%, var(--bg)); stroke: var(--accent); }
.heat.h3 { fill: color-mix(in srgb, var(--accent) 68%, var(--bg)); stroke: var(--accent); }
.heat.h4 { fill: var(--accent); stroke: var(--text); }
.heat.bulk { stroke-dasharray: 4 3; }
.heat.move-in { stroke: var(--ok); stroke-width: 4; stroke-dasharray: none; }
.heat.move-out { stroke: var(--stop); stroke-width: 3; stroke-dasharray: 2 3; }
.map-label { font: 500 12px var(--font-code); fill: var(--text-secondary); }
.map-n { font: 600 12px var(--font-code); fill: var(--text); }
.map-n.on-dark { fill: var(--on-accent); }
.legend { display: flex; flex-wrap: wrap; gap: 8px 16px; list-style: none; margin: 0; padding: 0; font-size: 13px; color: var(--text-secondary); }
.legend li { display: flex; align-items: center; gap: 6px; }

/* Login */
.auth { min-height: 100dvh; display: grid; place-items: center; padding: 24px 16px; background: var(--bg-subtle); }
.auth-card { width: 100%; max-width: 380px; display: flex; flex-direction: column; gap: 4px; padding: 28px; background: var(--bg); border: 1px solid var(--border); border-radius: var(--r-xl); box-shadow: var(--shadow-sm); }
.auth-card h1 { margin-top: 12px; font-size: 20px; line-height: 28px; }
.auth-card .form { margin-top: 16px; }

/* Scanner: one task per screen, primary action pinned to the bottom, nothing under 14px */
.scan { display: flex; flex-direction: column; min-height: 100dvh; font-size: 18px; line-height: 26px; background: var(--bg-subtle); }
.scan .label { font-size: 14px; line-height: 20px; font-weight: 600; }
.scan .tag { height: 26px; padding: 0 8px; font-size: 14px; }
.scan .chip { height: 28px; font-size: 14px; }
.scan .btn { height: 56px; padding: 0 20px; font-size: 18px; font-weight: 600; border-radius: var(--r-xl); }
.scan .btn.small { height: 48px; padding: 0 14px; font-size: 16px; border-radius: var(--r-lg); }
.scan .btn.small.icon { width: 48px; padding: 0; }
.scan .grow { flex: 1; }
.scan-head { display: flex; flex-direction: column; gap: 2px; padding: 12px 16px; background: var(--bg); border-bottom: 1px solid var(--border); }
.scan-head .code { display: block; font-size: 28px; line-height: 34px; font-weight: 600; }
.head-note { font-size: 16px; line-height: 22px; color: var(--text-secondary); }
.scan .code.code-s { font-size: 16px; line-height: 22px; font-weight: 600; }
.scan .code.code-m { font-size: 18px; line-height: 24px; font-weight: 600; }
.scan .code.code-l { font-size: 24px; line-height: 30px; font-weight: 600; }
.scan .sub { font-size: 14px; line-height: 20px; }
.scan-body { flex: 1; display: flex; flex-direction: column; gap: 16px; padding: 16px; }
.scan-bar { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 16px; font-size: 14px; font-weight: 600; background: var(--bg); border-top: 1px solid var(--border); }
.scan-bar .status { display: inline-flex; align-items: center; gap: 6px; }
.scan-actions { display: flex; align-items: center; gap: 12px; padding: 12px 16px calc(12px + env(safe-area-inset-bottom)); background: var(--bg); border-top: 1px solid var(--border); }
.scan-actions .btn.primary { flex: 1; }
.scan-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); }
.scan-grid .btn.primary { grid-column: 1 / -1; }
.scan-choice { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.scan-actions .btn.text { height: 56px; padding: 0 12px; font: 600 16px var(--font-ui); color: var(--stop); background: none; border: 0; box-shadow: none; }
.scan-actions .btn.text.back { color: var(--text-secondary); }
.scan-actions .btn.text:hover:not([disabled]) { background: var(--bg-hover); }
.scan-input { width: 100%; height: 56px; padding: 0 14px; font: 600 24px var(--font-code); color: var(--text); background: var(--bg); border: 2px solid var(--accent); border-radius: var(--r-xl); }
.scan-input:focus { outline: none; box-shadow: 0 0 0 4px color-mix(in srgb, var(--accent) 25%, transparent); }
.scan-input.qty { width: 120px; font-size: 28px; }
.scan-input.qty.qty-sm { width: 100px; }
.scan-target { font: 600 28px/34px var(--font-code); }
.scan-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; }
.scan-list button {
  width: 100%; min-height: 64px; padding: 12px 16px; text-align: left; cursor: pointer;
  font: 400 16px/22px var(--font-ui); color: var(--text); background: var(--bg);
  border: 1px solid var(--border-strong); border-radius: var(--r-xl); box-shadow: var(--shadow-xs);
}
.scan-list button:active { background: var(--bg-hover); }
.scan-list button[data-suggested="true"] { border: 2px solid var(--accent); }
.scan-list .panel { padding: 12px 16px; }
.scan-line { flex-direction: row; align-items: center; flex-wrap: wrap; }
.option-title { font-size: 22px; line-height: 28px; font-weight: 600; }
.scan-last { min-height: 28px; font: 600 16px var(--font-code); }
.scan-last[data-ok="true"] { color: var(--ok); }
.scan-last[data-ok="false"] { color: var(--stop); }
.stepper { display: flex; align-items: center; gap: 12px; }
.stepper button { width: 56px; height: 56px; font: 600 28px var(--font-ui); color: var(--text); background: var(--bg); border: 1px solid var(--border-strong); border-radius: var(--r-xl); box-shadow: var(--shadow-xs); cursor: pointer; }
.stepper input { width: 120px; height: 56px; text-align: center; font: 600 40px var(--font-code); color: var(--text); background: var(--bg); border: 2px solid var(--border-strong); border-radius: var(--r-xl); }
.steps { list-style: none; margin: 0; padding: 0; }
.step { display: flex; align-items: center; gap: 10px; padding: 8px 0; font-size: 18px; }
.todo-dot { flex: none; display: inline-block; width: 26px; height: 26px; border: 3px solid var(--text-secondary); border-radius: 50%; }
.main, .scan-body { scroll-margin-top: 64px; }

/* Scan result flash: whole screen edge for 200ms; off under reduced motion (sound and haptic remain) */
html[data-flash]::after { content: ""; position: fixed; inset: 0; z-index: 100; pointer-events: none; border: 10px solid transparent; }
html[data-flash="ok"]::after { border-color: var(--ok); }
html[data-flash="error"]::after { border-color: var(--stop); }
@media (prefers-reduced-motion: reduce) {
  html[data-flash]::after { display: none; }
  * { transition: none !important; }
}

/* Narrow screens */
@media (max-width: 800px) {
  .shell { grid-template-columns: 1fr; }
  .sidebar { position: static; height: auto; flex-direction: row; align-items: center; gap: 4px; padding: 6px 8px; overflow-x: auto; border-right: 0; border-bottom: 1px solid var(--border); }
  .nav-group { display: contents; }
  .nav-group-title { display: none; }
  .nav-item { flex: none; white-space: nowrap; }
  .ws { flex: none; min-width: 180px; }
  .sidebar-foot { flex-direction: row; align-items: center; margin: 0; padding: 0; border: 0; }
  .user-row { flex: none; }
  .split { grid-template-columns: 1fr; }
  .page-head { padding: 0 16px; }
  .page-body { padding: 12px 16px 24px; }
  .ageing { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}

/* Touch screens: console controls grow to finger size */
@media (pointer: coarse) {
  .btn { height: 44px; }
  .btn.small { height: 40px; }
  .btn.icon { width: 44px; }
  .btn.small.icon { width: 40px; }
  .input, .field input, .field select, .filters input, .filters select { height: 44px; font-size: 16px; }
  .input.small { height: 40px; font-size: 16px; }
  .nav-item { height: 44px; }
  .tabs a { height: 40px; }
  input[type="checkbox"] { width: 24px; height: 24px; }
}
```

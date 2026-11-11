/**
 * The one navigation model.
 *
 * The sidebar and command palette both read the
 * names, order and targets defined here, so a page cannot be called one thing
 * in the sidebar and another in the palette. Icons are referenced by key; the
 * sidebar owns the actual SVGs.
 *
 * Personal edition only. The server-edition menus (`/my/*`, `/admin/*`) keep
 * their own lists in SidebarNav.
 */
import type { RouteLocationRaw } from 'vue-router'
import type { Component } from 'vue'
import { Server, Users, Layers, Wrench, KeyRound, Activity, ChartNoAxesCombined } from '@lucide/vue'

export type NavIcon = Component

export type NavBadge = 'review' | 'failed'

export interface NavItem {
  id: string
  label: string
  /** Router path. */
  path: string
  icon: NavIcon
  badge?: NavBadge
  /**
   * Item is shown only when the router has a route with exactly this path.
   * Profiles ships hidden until `/profiles` is registered.
   */
  requiresRoutePath?: string
}

export interface NavGroup {
  id: 'connect' | 'protect' | 'monitor'
  label: string
  items: NavItem[]
}

export const SIDEBAR_GROUPS: NavGroup[] = [
  {
    id: 'connect',
    label: 'Connect',
    items: [
      { id: 'servers', label: 'Servers', path: '/servers', icon: Server, badge: 'failed' },
      { id: 'clients', label: 'Clients', path: '/clients', icon: Users },
      {
        id: 'profiles',
        label: 'Profiles',
        path: '/profiles',
        icon: Layers,
        requiresRoutePath: '/profiles'
      },
      {
        id: 'tools',
        label: 'Tools',
        path: '/tools',
        icon: Wrench,
        badge: 'review'
      }
    ]
  },
  {
    id: 'protect',
    label: 'Protect',
    items: [{ id: 'secrets', label: 'Secrets', path: '/secrets', icon: KeyRound }]
  },
  {
    id: 'monitor',
    label: 'Monitor',
    items: [
      {
        id: 'activity',
        label: 'Activity',
        path: '/activity',
        icon: Activity
      },
      { id: 'usage', label: 'Usage', path: '/usage', icon: ChartNoAxesCombined }
    ]
  }
]

export interface PaletteAction {
  id: string
  label: string
  to?: RouteLocationRaw
  requiresRoutePath?: string
}

/** Keyboard palette actions. */
export const PALETTE_ACTIONS: PaletteAction[] = [
  { id: 'add-server', label: 'Add server', to: { path: '/servers', query: { add: '1' } } },
  {
    id: 'create-client',
    label: 'Add client',
    to: { path: '/clients', query: { create: '1' } }
  },
  {
    id: 'open-review',
    label: 'Review tools',
    to: { path: '/tools', query: { review: '1' } }
  },
  {
    id: 'create-profile',
    label: 'Create profile',
    to: { path: '/profiles', query: { create: '1' } },
    requiresRoutePath: '/profiles'
  }
]

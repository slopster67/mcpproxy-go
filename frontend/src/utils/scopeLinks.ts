import type { RouteLocationRaw } from 'vue-router'

/** Open a saved client without carrying unrelated scope filters. */
export function clientLink(id: string): RouteLocationRaw {
 return {path:'/clients',query:{client:id}}
}

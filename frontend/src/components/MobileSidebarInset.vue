<template>
  <SidebarInset
    @touchstart.passive="start"
    @touchmove.passive="move"
    @touchend.passive="end"
    @touchcancel.passive="cancel"
  >
    <slot />
  </SidebarInset>
</template>
<script setup lang="ts">
import { SidebarInset, useSidebar } from "@/components/ui/sidebar";
const { isMobile, openMobile, setOpenMobile } = useSidebar();
let gesture: { id: number; x: number; y: number; started: number } | null =
  null;
function cancel() {
  gesture = null;
}
function start(event: TouchEvent) {
  cancel();
  if (!isMobile.value || openMobile.value || event.touches.length !== 1) return;
  const touch = event.touches[0]!;
  // Leave both screen edges to the browser's Back/Forward gestures.
  if (touch.clientX <= 32 || touch.clientX >= window.innerWidth - 32) return;
  const target = event.target instanceof Element ? event.target : null;
  if (
    !target ||
    target.closest(
      'input, textarea, select, [contenteditable], [role="slider"]',
    )
  )
    return;
  for (let node: Element | null = target; node; node = node.parentElement) {
    if (
      node.scrollWidth > node.clientWidth + 1 &&
      /auto|scroll/.test(getComputedStyle(node).overflowX)
    )
      return;
    if (node.matches('[data-slot="sidebar-inset"]')) break;
  }
  gesture = {
    id: touch.identifier,
    x: touch.clientX,
    y: touch.clientY,
    started: event.timeStamp,
  };
}
function move(event: TouchEvent) {
  if (!gesture) return;
  if (event.touches.length !== 1) {
    cancel();
    return;
  }
  const touch = Array.from(event.touches).find(
    (touch) => touch.identifier === gesture!.id,
  );
  if (!touch) {
    cancel();
    return;
  }
  const dx = touch.clientX - gesture.x,
    dy = touch.clientY - gesture.y;
  if (dx < -12 || (Math.abs(dy) > 12 && Math.abs(dy) > Math.abs(dx))) cancel();
}
function end(event: TouchEvent) {
  const current = gesture;
  cancel();
  if (
    !current ||
    !isMobile.value ||
    openMobile.value ||
    document.getSelection()?.toString()
  )
    return;
  const touch = Array.from(event.changedTouches).find(
    (touch) => touch.identifier === current.id,
  );
  if (!touch) return;
  const dx = touch.clientX - current.x,
    dy = touch.clientY - current.y;
  if (
    dx >= 72 &&
    dx > Math.abs(dy) * 1.5 &&
    event.timeStamp - current.started < 900
  )
    setOpenMobile(true);
}
</script>

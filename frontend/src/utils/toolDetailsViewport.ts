const margin = 400;
const waiting = new Map<Element, () => void>();
let observer: IntersectionObserver | undefined;
function remove(element: Element) {
  waiting.delete(element);
  observer?.unobserve(element);
  if (!waiting.size) {
    observer?.disconnect();
    observer = undefined;
  }
}
export function whenToolDetailsNearViewport(
  element: HTMLElement,
  reveal: () => void,
) {
  const rect = element.getBoundingClientRect();
  if (rect.top <= window.innerHeight + margin && rect.bottom >= -margin) {
    reveal();
    return () => {};
  }
  if (!observer)
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const show = waiting.get(entry.target);
          if (entry.isIntersecting && show) {
            remove(entry.target);
            show();
          }
        }
      },
      { rootMargin: `${margin}px` },
    );
  waiting.set(element, reveal);
  observer.observe(element);
  return () => remove(element);
}

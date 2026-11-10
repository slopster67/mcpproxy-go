/** Call directly from a click/tap so Safari retains the user gesture. */
export async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }

  // HTTP LAN origins have no asynchronous Clipboard API. Copy the selected
  // text synchronously, within the original tap, without opening the keyboard.
  const active = document.activeElement as HTMLElement | null;
  const selection = document.getSelection();
  const ranges = selection
    ? Array.from({ length: selection.rangeCount }, (_, i) =>
        selection.getRangeAt(i).cloneRange(),
      )
    : [];
  const input = document.createElement("textarea");
  input.value = value;
  input.readOnly = true;
  input.tabIndex = -1;
  input.setAttribute("aria-hidden", "true");
  input.setAttribute("data-1p-ignore", "");
  Object.assign(input.style, {
    position: "fixed",
    top: "0",
    left: "0",
    width: "1px",
    height: "1px",
    opacity: "0",
    fontSize: "16px",
  });
  const container = active?.closest('[role="dialog"], dialog') ?? document.body;
  container.appendChild(input);
  try {
    input.focus({ preventScroll: true });
    input.select();
    const range = document.createRange();
    range.selectNodeContents(input);
    selection?.removeAllRanges();
    selection?.addRange(range);
    input.setSelectionRange(0, value.length);
    if (!document.execCommand("copy"))
      throw new Error("Clipboard copy was refused");
  } finally {
    input.remove();
    if (active?.isConnected) active.focus({ preventScroll: true });
    selection?.removeAllRanges();
    for (const range of ranges) selection?.addRange(range);
  }
}

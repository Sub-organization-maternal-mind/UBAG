<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';

  let {
    open = $bindable(false),
    title = '',
    width = 'md',
    onClose,
    children,
    footer,
  }: {
    open?: boolean;
    title?: string;
    width?: 'sm' | 'md' | 'lg' | 'xl';
    onClose?: () => void;
    children: Snippet;
    footer?: Snippet;
  } = $props();

  const widthClass = {
    sm: 'max-w-md',
    md: 'max-w-lg',
    lg: 'max-w-2xl',
    xl: 'max-w-4xl',
  } as const;

  let dialogEl: HTMLDialogElement | undefined = $state();

  $effect(() => {
    if (!dialogEl) return;
    if (open && !dialogEl.open) dialogEl.showModal();
    else if (!open && dialogEl.open) dialogEl.close();
  });

  // Native close paths: Escape, our own close button, programmatic .close().
  function handleNativeClose() {
    if (open) {
      open = false;
      onClose?.();
    }
  }
</script>

<dialog
  bind:this={dialogEl}
  onclose={handleNativeClose}
  class="ubag-modal w-[calc(100vw-2rem)] {widthClass[width]} flex-col max-h-[min(85dvh,52rem)]"
>
  <header class="flex items-center justify-between gap-4 border-b border-rule px-5 py-3.5 shrink-0">
    <h2 class="text-base font-display font-semibold text-ink min-w-0 truncate">{title}</h2>
    <button
      type="button"
      class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-ink-soft hover:bg-rule-soft hover:text-ink transition-colors"
      aria-label="Close dialog"
      onclick={() => dialogEl?.close()}
    >
      <X class="h-5 w-5" aria-hidden="true" />
    </button>
  </header>

  <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
    {@render children()}
  </div>

  {#if footer}
    <footer class="flex items-center justify-end gap-2 border-t border-rule px-5 py-3.5 shrink-0">
      {@render footer()}
    </footer>
  {/if}
</dialog>

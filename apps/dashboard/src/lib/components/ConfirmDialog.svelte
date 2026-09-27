<script lang="ts">
  import Modal from './Modal.svelte';

  let {
    open = $bindable(false),
    title = 'Are you sure?',
    message = '',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    danger = false,
    pending = false,
    error = '',
    onconfirm,
  }: {
    open?: boolean;
    title?: string;
    message?: string;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    pending?: boolean;
    error?: string;
    onconfirm?: () => void;
  } = $props();
</script>

<Modal bind:open {title} width="sm">
  <p class="text-sm text-ink-soft whitespace-pre-wrap">{message}</p>
  {#if error}
    <p class="mt-2 text-sm text-danger" role="alert">{error}</p>
  {/if}
  {#snippet footer()}
    <button type="button" class="btn btn-secondary" onclick={() => (open = false)} disabled={pending}>
      {cancelLabel}
    </button>
    <button
      type="button"
      class="btn {danger ? 'btn-danger' : 'btn-primary'}"
      onclick={onconfirm}
      disabled={pending}
    >
      {pending ? 'Working…' : confirmLabel}
    </button>
  {/snippet}
</Modal>

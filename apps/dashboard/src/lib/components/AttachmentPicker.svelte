<!-- Hallmark · pre-emit critique: P5 H4 E5 S5 R5 V4 -->
<script lang="ts">
  import {
    humanFileSize,
    validateAttachmentFiles,
    type SelectedAttachment,
  } from '$lib/attachments';

  type PickerState = 'default' | 'hover' | 'focus' | 'active' | 'disabled' | 'loading' | 'error' | 'success';

  let {
    disabled = false,
    loading = false,
    error = null,
    success = null,
    forcedState = null,
    onchange,
  }: {
    disabled?: boolean;
    loading?: boolean;
    error?: string | null;
    success?: string | null;
    forcedState?: PickerState | null;
    onchange?: (attachments: SelectedAttachment[], error: string | null) => void;
  } = $props();

  let inputEl = $state<HTMLInputElement | null>(null);
  let attachments = $state<SelectedAttachment[]>([]);
  let localError = $state<string | null>(null);
  let dragging = $state(false);

  let visualState: PickerState = $derived(
    forcedState ??
      (disabled ? 'disabled' : loading ? 'loading' : error || localError ? 'error' : success ? 'success' : dragging ? 'active' : 'default')
  );
  let describedBy = $derived(error || localError ? 'attachment-error' : 'attachment-help');

  function acceptFiles(files: File[]) {
    if (disabled || loading) return;
    const result = validateAttachmentFiles(files);
    localError = result.error;
    attachments = result.attachments;
    onchange?.(attachments, localError);
  }

  function handleInput(event: Event) {
    acceptFiles(Array.from((event.currentTarget as HTMLInputElement).files ?? []));
  }

  function handleDrop(event: DragEvent) {
    event.preventDefault();
    dragging = false;
    acceptFiles(Array.from(event.dataTransfer?.files ?? []));
  }

  function removeAttachment(key: string) {
    acceptFiles(attachments.filter((attachment) => attachment.key !== key).map(({ file }) => file));
    if (inputEl) inputEl.value = '';
  }

  export function clear() {
    attachments = [];
    localError = null;
    dragging = false;
    if (inputEl) inputEl.value = '';
    onchange?.([], null);
  }
</script>

<div class="attachment-picker" data-state={visualState}>
  <label
    class="drop-zone"
    class:is-hover={forcedState === 'hover'}
    class:is-focus={forcedState === 'focus'}
    class:is-active={forcedState === 'active'}
    aria-disabled={disabled || loading}
    aria-busy={loading}
    ondragover={(event) => {
      event.preventDefault();
      if (!disabled && !loading) dragging = true;
    }}
    ondragleave={() => { dragging = false; }}
    ondrop={handleDrop}
  >
    <input
      bind:this={inputEl}
      type="file"
      multiple
      disabled={disabled || loading}
      aria-invalid={error || localError ? 'true' : 'false'}
      aria-describedby={describedBy}
      onchange={handleInput}
    />
    <span class="drop-zone__mark" aria-hidden="true">{loading ? '···' : '+'}</span>
    <span class="drop-zone__copy">
      <strong>{loading ? 'Preparing attachments…' : 'Drop files here or browse'}</strong>
      <span>Documents, images, audio, voice, and video</span>
    </span>
  </label>

  <p id="attachment-help" class="picker-help">
    Up to 10 files · 32 MiB each · 320 MiB total. File type is verified from the extension.
  </p>

  {#if error || localError}
    <p id="attachment-error" class="picker-message picker-message--error" role="alert">
      <span aria-hidden="true">!</span> {error ?? localError}
    </p>
  {:else if success}
    <p class="picker-message picker-message--success" role="status">
      <span aria-hidden="true">✓</span> {success}
    </p>
  {/if}

  {#if attachments.length > 0}
    <div class="selection-head">
      <span>{attachments.length} selected</span>
      <button type="button" class="clear-button" onclick={clear} disabled={disabled || loading}>Clear all</button>
    </div>
    <ul class="attachment-list" aria-label="Selected attachments">
      {#each attachments as attachment (attachment.key)}
        <li>
          <span class="file-meta">
            <strong title={attachment.key}>{attachment.key}</strong>
            <span>{attachment.kind} · {humanFileSize(attachment.file.size)}</span>
          </span>
          <button
            type="button"
            class="remove-button"
            aria-label={`Remove ${attachment.key}`}
            disabled={disabled || loading}
            onclick={() => removeAttachment(attachment.key)}
          >×</button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  /* Hallmark · component: file drop zone · genre: editorial · theme: NAJM
   * states: default · hover · focus · active · disabled · loading · error · success
   * contrast: pass (46–50)
   */
  .attachment-picker {
    min-width: 0;
    color: var(--color-ink);
  }

  .drop-zone {
    display: flex;
    min-height: 7rem;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-4);
    border: 1px dashed var(--color-rule);
    border-radius: var(--radius-md);
    background: var(--color-paper);
    color: var(--color-ink);
    cursor: pointer;
    outline: 2px solid transparent;
    outline-offset: 2px;
    transition:
      background-color var(--dur-base) var(--ease-out),
      transform var(--dur-fast) var(--ease-out);
  }

  .drop-zone input {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }

  .drop-zone__mark {
    display: grid;
    width: 2.75rem;
    height: 2.75rem;
    flex: 0 0 auto;
    place-items: center;
    border: 1px solid var(--color-rule);
    border-radius: var(--radius-sm);
    background: var(--color-paper-warm);
    color: var(--color-accent-deep);
    font-family: var(--font-display);
    font-size: 1.5rem;
    line-height: 1;
  }

  .drop-zone__copy {
    display: grid;
    min-width: 0;
    gap: var(--space-1);
  }

  .drop-zone__copy strong {
    overflow-wrap: anywhere;
    font-family: var(--font-display);
    font-size: 0.95rem;
  }

  .drop-zone__copy span,
  .picker-help,
  .file-meta span {
    color: var(--color-ink-mute);
    font-size: 0.75rem;
  }

  .picker-help {
    min-height: 1lh;
    margin: var(--space-2) 0 0;
  }

  .picker-message {
    display: flex;
    min-height: 1.75rem;
    align-items: center;
    gap: var(--space-2);
    margin: var(--space-2) 0 0;
    font-size: 0.78rem;
  }

  .picker-message--error {
    color: var(--color-danger);
  }

  .picker-message--success {
    color: var(--color-success);
  }

  .selection-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    margin-top: var(--space-3);
    color: var(--color-ink-soft);
    font-family: var(--font-mono);
    font-size: 0.75rem;
  }

  .clear-button,
  .remove-button {
    min-width: 2.75rem;
    min-height: 2.75rem;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--color-accent-deep);
    cursor: pointer;
    font: inherit;
  }

  .clear-button {
    padding-inline: var(--space-3);
    white-space: nowrap;
  }

  .attachment-list {
    display: grid;
    gap: 0;
    margin: var(--space-1) 0 0;
    padding: 0;
    border-top: 1px solid var(--color-rule-soft);
    list-style: none;
  }

  .attachment-list li {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding-block: var(--space-2);
    border-bottom: 1px solid var(--color-rule-soft);
  }

  .file-meta {
    display: grid;
    min-width: 0;
    gap: var(--space-1);
  }

  .file-meta strong {
    overflow: hidden;
    color: var(--color-ink-soft);
    font-size: 0.8rem;
    font-weight: 600;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  @media (hover: hover) and (pointer: fine) {
    .drop-zone:hover,
    .drop-zone.is-hover {
      background: var(--color-paper-soft);
      color: var(--color-ink);
      transform: translateY(-1px);
    }

    .clear-button:hover,
    .remove-button:hover {
      background: var(--color-paper-warm);
      color: var(--color-accent-deep);
    }
  }

  .drop-zone:has(input:focus-visible),
  .drop-zone.is-focus {
    outline-color: var(--color-focus-ring);
  }

  .drop-zone:active,
  .drop-zone.is-active,
  .attachment-picker[data-state='active'] .drop-zone {
    background: var(--color-paper-warm);
    color: var(--color-ink);
    transform: translateY(1px);
  }

  .attachment-picker[data-state='disabled'] .drop-zone {
    cursor: not-allowed;
    opacity: 0.5;
  }

  .attachment-picker[data-state='loading'] .drop-zone {
    background: var(--color-paper-soft);
    color: var(--color-ink);
    cursor: progress;
  }

  .attachment-picker[data-state='error'] .drop-zone {
    border-color: var(--color-danger);
    background: var(--color-danger-soft);
    color: var(--color-ink);
  }

  .attachment-picker[data-state='success'] .drop-zone {
    border-color: var(--color-success);
    background: var(--color-success-soft);
    color: var(--color-ink);
  }

  button:focus-visible {
    outline: 2px solid var(--color-focus-ring);
    outline-offset: 2px;
  }

  button:active {
    transform: translateY(1px);
  }

  button:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }

  @media (min-width: 40rem) {
    .drop-zone {
      padding: var(--space-5);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .drop-zone {
      transition: background-color 100ms var(--ease-out);
    }
  }
</style>

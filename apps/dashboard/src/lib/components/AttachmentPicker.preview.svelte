<!-- Hallmark · component preview: attachment picker · all eight states -->
<script lang="ts">
  import AttachmentPicker from './AttachmentPicker.svelte';

  const states = ['default', 'hover', 'focus', 'active', 'disabled', 'loading', 'error', 'success'] as const;
</script>

<div class="preview-grid">
  {#each states as state}
    <section>
      <h2>{state}</h2>
      <AttachmentPicker
        forcedState={state}
        disabled={state === 'disabled'}
        loading={state === 'loading'}
        error={state === 'error' ? 'One file has an unsupported extension. Remove it and choose a supported file.' : null}
        success={state === 'success' ? 'Attachments are ready.' : null}
      />
    </section>
  {/each}
</div>

<style>
  .preview-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: var(--space-6);
    padding: var(--space-4);
    background: var(--color-paper);
    color: var(--color-ink);
  }

  section,
  h2 {
    min-width: 0;
    margin: 0;
  }

  h2 {
    margin-bottom: var(--space-2);
    font-family: var(--font-mono);
    font-size: 0.75rem;
    text-transform: uppercase;
  }

  @media (min-width: 48rem) {
    .preview-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>

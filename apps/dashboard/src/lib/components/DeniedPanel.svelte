<script lang="ts">
  let { resource = 'resource', message = '' }: { resource?: string; message?: string } = $props();
</script>

<div class="rounded-md border border-danger/30 bg-danger-soft p-5 text-sm" role="alert" aria-label="Access denied">
  <div class="flex items-start gap-3">
    <svg class="w-5 h-5 text-danger shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L3.07 16.5c-.77.833.192 2.5 1.732 2.5z" />
    </svg>
    <div>
      <p class="font-medium text-danger">Access denied</p>
      <p class="text-ink-soft mt-1">
        {message || `Your role does not have permission to view ${resource}. Contact your administrator or use a higher-privilege app secret.`}
      </p>
    </div>
  </div>
</div>

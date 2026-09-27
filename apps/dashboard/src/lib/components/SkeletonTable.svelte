<script lang="ts">
  let { rows = 5, cols = 4 }: { rows?: number; cols?: number } = $props();
</script>

<div class="table-wrap" role="status" aria-label="Loading">
  <span class="sr-only">Loading…</span>
  <table class="w-full text-sm" aria-hidden="true">
    <thead class="thead">
      <tr>
        {#each Array(cols) as _}
          <th class="th"><span class="skeleton block h-3 w-16"></span></th>
        {/each}
      </tr>
    </thead>
    <tbody class="divide-y divide-rule">
      {#each Array(rows) as _}
        <tr>
          {#each Array(cols) as __, c}
            <td class="td">
              <span class="skeleton block h-3.5" style="width: {55 + ((c * 13 + 17) % 40)}%"></span>
            </td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

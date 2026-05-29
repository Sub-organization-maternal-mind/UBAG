<script lang="ts">
  import { statusTone, humanize } from "../lib/format";

  export let status: string;
  export let label: string | undefined = undefined;
</script>

<span class="badge" data-tone={statusTone(status)}>
  <span class="dot" data-tone={statusTone(status)} aria-hidden="true"></span>
  {label ?? humanize(status)}
</span>

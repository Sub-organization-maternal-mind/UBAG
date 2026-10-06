#!/usr/bin/env node
// Stand-in for `pactl` (UBAG_VOICE_PACTL): reports the relay's virtual mic and provider sink (and its monitor) as present, so
// the relay's device health loop is satisfied without PulseAudio; every other subcommand succeeds silently.
const [cmd, sub, kind] = process.argv.slice(2);
const line = (id, name) => `${id}\t${name}\tmodule-shim.c\ts16le 1ch 48000Hz\tRUNNING\n`;
if (cmd === 'list' && sub === 'short' && kind === 'sources') process.stdout.write(line(0, 'ubag_virtual_mic') + line(1, 'ubag_provider_sink.monitor'));
else if (cmd === 'list' && sub === 'short' && kind === 'sinks') process.stdout.write(line(0, 'ubag_provider_sink'));
else if (cmd === 'info') process.stdout.write('Server Name: ubag-bench-shim\n');

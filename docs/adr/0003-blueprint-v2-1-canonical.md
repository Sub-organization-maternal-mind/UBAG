# Blueprint v2.1 is canonical; §12 defines the concurrency model

`UBAG_World_Class_Blueprint_v2.1.md` supersedes v2.0 (README's pointer to v2.0 is stale). Its §12 three-level browser model — browser instance → provider context → channel tab, with invariants INV-1..INV-5 — is the adopted concurrency model; v2.0's "browser session" is the compatibility alias of provider context. Considered: keeping v2.0 canonical (rejected — the v2.1 model is what the worker actually implements: ChannelPool, AIMD, topology reporting).

Status: accepted

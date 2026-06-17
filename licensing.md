# UBAG License Posture

## License Split

UBAG uses a dual-license model:

| Component | License | Rationale |
|-----------|---------|-----------|
| Gateway (`apps/gateway/`) | AGPL-3.0 | Server-side execution triggers copyleft |
| Worker (`apps/worker/`) | AGPL-3.0 | Server-side, same rationale |
| TypeScript SDK (`packages/sdk-typescript/`) | Apache-2.0 | Client library; permissive |
| Go SDK (`packages/sdk-go/`) | Apache-2.0 | Client library; permissive |
| Sidecar (`packages/sidecar-rust/`) | Apache-2.0 | Agent runtime; permissive |
| Dashboard (`apps/dashboard/`) | Apache-2.0 | Frontend; permissive |
| Mobile (`apps/mobile/`) | Apache-2.0 | Frontend; permissive |
| CLI (`packages/cli/`) | Apache-2.0 | Client tool; permissive |
| Docs (`apps/docs/`) | Apache-2.0 | Documentation; permissive |
| Adapters (`adapters/`) | Apache-2.0 | Provider adapters; permissive |

## SPDX identifiers

Each source file includes an SPDX header:
- Server components: `SPDX-License-Identifier: AGPL-3.0-only`
- Client components: `SPDX-License-Identifier: Apache-2.0`

## Why AGPL for the server?

The AGPL ensures that anyone running a modified version of the UBAG gateway as a service must publish their modifications. This protects the open-source investment in the gateway while allowing unlimited use.

## Release cadence

- **Minor** (`vX.Y.0`): Quarterly, aligned with blueprint phase completion
- **Patch** (`vX.Y.Z`): Monthly or as needed for bug fixes
- **LTS**: Every other minor release receives 12 months of security support

## Version policy

- API version (`Ubag-Api-Version` header): date-based (`YYYY-MM-DD`)
- Binary version: semver (`vMAJOR.MINOR.PATCH`)
- API version changes on breaking API changes; binary version follows conventional semver

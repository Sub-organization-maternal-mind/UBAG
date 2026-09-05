// Job-status vocabulary consumed from the SDK's generated contract manifest
// (ADR-0004: one vocabulary source). Never hand-write status lists here.
import {
  UBAG_JOB_STATUSES,
  UBAG_TERMINAL_JOB_STATUSES,
} from '@ubag/sdk/contract-manifest';

export const JOB_STATUSES = Object.keys(UBAG_JOB_STATUSES) as string[];

export const TERMINAL_STATUSES: ReadonlySet<string> = new Set(UBAG_TERMINAL_JOB_STATUSES);

export const FAILED_STATES: ReadonlySet<string> = new Set(
  JOB_STATUSES.filter(
    (s) =>
      UBAG_JOB_STATUSES[s as keyof typeof UBAG_JOB_STATUSES].terminal &&
      s !== 'completed' &&
      s !== 'completed_with_warnings' &&
      s !== 'cancelled'
  )
);

export function isTerminalStatus(status: string | undefined | null): boolean {
  if (!status) return false;
  return TERMINAL_STATUSES.has(status.toLowerCase());
}

export function isFailedStatus(status: string | undefined | null): boolean {
  if (!status) return false;
  return FAILED_STATES.has(status.toLowerCase());
}

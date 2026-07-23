import { describe, expect, it } from 'vitest';
import {
  ATTACHMENT_MAX_FILE_BYTES,
  DASHBOARD_ATTACHMENT_MAX_FILES,
  resolveAttachmentPickerState,
  validateAttachmentFiles,
} from './attachments';

describe('dashboard attachment validation', () => {
  it('maps known files to manifest metadata', () => {
    const result = validateAttachmentFiles([
      new File(['report'], 'report.pdf', { type: 'application/pdf' }),
      new File(['voice'], 'note.webm', { type: 'audio/webm' }),
    ]);

    expect(result.error).toBeNull();
    expect(result.attachments.map(({ key, contentType, kind }) => ({ key, contentType, kind }))).toEqual([
      { key: 'report.pdf', contentType: 'application/pdf', kind: 'document' },
      { key: 'note.webm', contentType: 'audio/webm', kind: 'voice' },
    ]);
  });

  it('rejects duplicate keys, unknown extensions, count, and size limits', () => {
    expect(validateAttachmentFiles([
      new File(['a'], 'same.txt'),
      new File(['b'], 'same.txt'),
    ]).error).toMatch(/duplicate/i);
    expect(validateAttachmentFiles([new File(['a'], 'archive.zip')]).error).toMatch(/supported/i);
    expect(validateAttachmentFiles(
      Array.from({ length: DASHBOARD_ATTACHMENT_MAX_FILES + 1 }, (_, index) =>
        new File(['a'], `${index}.txt`),
      ),
    ).error).toMatch(/10 files/i);
    const tooLarge = new File(['x'], 'large.pdf');
    Object.defineProperty(tooLarge, 'size', { value: ATTACHMENT_MAX_FILE_BYTES + 1 });
    expect(validateAttachmentFiles([tooLarge]).error).toMatch(/32 MiB/i);
  });

  it('keeps the loading state reachable while the route disables in-flight selection', () => {
    expect(resolveAttachmentPickerState({
      disabled: true,
      loading: true,
      hasError: false,
      hasSuccess: false,
      dragging: false,
    })).toBe('loading');
  });
});

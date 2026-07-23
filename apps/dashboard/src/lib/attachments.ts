export const DASHBOARD_ATTACHMENT_MAX_FILES = 10;
export const ATTACHMENT_MAX_FILE_BYTES = 32 * 1024 * 1024;
export const DASHBOARD_ATTACHMENT_MAX_TOTAL_BYTES =
  DASHBOARD_ATTACHMENT_MAX_FILES * ATTACHMENT_MAX_FILE_BYTES;

export type AttachmentKind = 'document' | 'image' | 'audio' | 'video' | 'voice';
export type AttachmentPickerState =
  | 'default'
  | 'hover'
  | 'focus'
  | 'active'
  | 'disabled'
  | 'loading'
  | 'error'
  | 'success';

export function resolveAttachmentPickerState({
  disabled,
  loading,
  hasError,
  hasSuccess,
  dragging,
}: {
  disabled: boolean;
  loading: boolean;
  hasError: boolean;
  hasSuccess: boolean;
  dragging: boolean;
}): AttachmentPickerState {
  if (loading) return 'loading';
  if (disabled) return 'disabled';
  if (hasError) return 'error';
  if (hasSuccess) return 'success';
  if (dragging) return 'active';
  return 'default';
}

export interface SelectedAttachment {
  file: File;
  key: string;
  contentType: string;
  kind: AttachmentKind;
}

const CONTENT_TYPES: Record<string, { contentType: string; kind: AttachmentKind }> = {
  pdf: { contentType: 'application/pdf', kind: 'document' },
  txt: { contentType: 'text/plain', kind: 'document' },
  md: { contentType: 'text/markdown', kind: 'document' },
  csv: { contentType: 'text/csv', kind: 'document' },
  json: { contentType: 'application/json', kind: 'document' },
  png: { contentType: 'image/png', kind: 'image' },
  jpg: { contentType: 'image/jpeg', kind: 'image' },
  jpeg: { contentType: 'image/jpeg', kind: 'image' },
  gif: { contentType: 'image/gif', kind: 'image' },
  webp: { contentType: 'image/webp', kind: 'image' },
  webm: { contentType: 'audio/webm', kind: 'voice' },
  wav: { contentType: 'audio/wav', kind: 'voice' },
  mp3: { contentType: 'audio/mpeg', kind: 'voice' },
  m4a: { contentType: 'audio/mp4', kind: 'voice' },
  ogg: { contentType: 'audio/ogg', kind: 'voice' },
  mp4: { contentType: 'video/mp4', kind: 'video' },
};

export function validateAttachmentFiles(_files: File[]): {
  attachments: SelectedAttachment[];
  error: string | null;
} {
  const files = [..._files];
  if (files.length > DASHBOARD_ATTACHMENT_MAX_FILES) {
    return { attachments: [], error: 'Choose at most 10 files for these providers.' };
  }

  const keys = new Set<string>();
  const attachments: SelectedAttachment[] = [];
  let totalBytes = 0;
  for (const file of files) {
    if (keys.has(file.name)) {
      return { attachments: [], error: `Remove duplicate filename "${file.name}".` };
    }
    keys.add(file.name);
    if (file.size > ATTACHMENT_MAX_FILE_BYTES) {
      return { attachments: [], error: `"${file.name}" exceeds the 32 MiB per-file limit.` };
    }
    totalBytes += file.size;
    if (totalBytes > DASHBOARD_ATTACHMENT_MAX_TOTAL_BYTES) {
      return { attachments: [], error: 'Attachments exceed the 320 MiB total limit.' };
    }
    const extension = file.name.toLowerCase().split('.').pop() ?? '';
    const metadata = CONTENT_TYPES[extension];
    if (!metadata) {
      return {
        attachments: [],
        error: `"${file.name}" is not a supported document, image, audio, voice, or video file.`,
      };
    }
    const normalizedFile =
      file.type === metadata.contentType
        ? file
        : new File([file], file.name, {
            type: metadata.contentType,
            lastModified: file.lastModified,
          });
    attachments.push({
      file: normalizedFile,
      key: file.name,
      contentType: metadata.contentType,
      kind: metadata.kind,
    });
  }
  return { attachments, error: null };
}

export function humanFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

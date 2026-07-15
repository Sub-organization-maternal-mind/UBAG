import { api } from '$lib/api/client';
import type { Conversation, ConversationsResponse } from '$lib/api/types';

/**
 * The rendered outcome of loading GET /v1/conversations, as a discriminated
 * union so the page never has to fabricate rows for a non-200 response.
 */
export type ConversationsView =
  | { kind: 'ok'; conversations: Conversation[]; nextCursor: string | null }
  | { kind: 'disabled' }
  | { kind: 'denied' }
  | { kind: 'error'; message: string };

/**
 * Load the conversation bindings from GET /v1/conversations and map the raw
 * gateway envelope into a view state.
 *
 * A 501 is an honest, expected response: conversation affinity is inert unless
 * the operator sets UBAG_CONVERSATIONS_ENABLED=true on the gateway. It surfaces
 * as `disabled` — never an error, and never a list of fabricated rows.
 */
export async function loadConversations(cursor?: string): Promise<ConversationsView> {
  const path = cursor
    ? `/v1/conversations?cursor=${encodeURIComponent(cursor)}&limit=20`
    : '/v1/conversations?limit=20';
  const res = await api.get<ConversationsResponse>(path);

  if (res.denied) return { kind: 'denied' };
  // 501 Not Implemented — conversations are not enabled on this gateway.
  if (res.status === 501) return { kind: 'disabled' };
  if (res.error) return { kind: 'error', message: res.error };

  return {
    kind: 'ok',
    conversations: res.data?.conversations ?? [],
    nextCursor: res.data?.next_cursor ?? null,
  };
}

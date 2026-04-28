/**
 * JMAP Core types per RFC 8620.
 *
 * Just the wire shapes the suite cares about for bootstrap and method-call
 * dispatch. Full RFC 8621 (Mail) datatypes layer on top in feature-specific
 * modules.
 */

/**
 * RFC 8620 §3.2 — A method call is a triple `[methodName, args, callId]`.
 */
export type Invocation<TArgs = unknown> = [
  methodName: string,
  args: TArgs,
  callId: string,
];

/**
 * RFC 8620 §3.7 — A result reference points at the result of a prior call
 * in the same batch via JSON-Pointer path.
 */
export interface ResultReference {
  resultOf: string;
  name: string;
  path: string;
}

/**
 * RFC 8620 §3.6 — Method-level error envelope. Wrapped as an Invocation
 * with name "error".
 */
export interface MethodError {
  type: string;
  description?: string;
}

/**
 * RFC 8620 §2 — Session resource. Returned from `GET /.well-known/jmap`.
 */
export interface SessionResource {
  capabilities: Record<string, unknown>;
  accounts: Record<string, AccountInfo>;
  primaryAccounts: Record<string, string>;
  username: string;
  apiUrl: string;
  downloadUrl: string;
  uploadUrl: string;
  eventSourceUrl: string;
  state: string;
}

export interface AccountInfo {
  name: string;
  isPersonal: boolean;
  isReadOnly: boolean;
  accountCapabilities: Record<string, unknown>;
}

/**
 * Request to `POST /jmap` per RFC 8620 §3.3.
 */
export interface MethodCallRequest {
  using: string[];
  methodCalls: Invocation[];
  createdIds?: Record<string, string>;
}

export interface MethodCallResponse {
  methodResponses: Invocation[];
  createdIds?: Record<string, string>;
  sessionState: string;
}

/**
 * Standard JMAP capability URIs we use across the suite.
 */
export const Capability = {
  Core: 'urn:ietf:params:jmap:core',
  Mail: 'urn:ietf:params:jmap:mail',
  Submission: 'urn:ietf:params:jmap:submission',
  Sieve: 'urn:ietf:params:jmap:sieve',
  VacationResponse: 'urn:ietf:params:jmap:vacationresponse',
  Calendars: 'urn:ietf:params:jmap:calendars',
  Contacts: 'urn:ietf:params:jmap:contacts',
  WebSocket: 'urn:ietf:params:jmap:websocket',
  // Herold suite-specific capabilities
  HeroldSnooze: 'https://netzhansa.com/jmap/snooze',
  HeroldCategorise: 'https://netzhansa.com/jmap/categorise',
  HeroldChat: 'https://netzhansa.com/jmap/chat',
  HeroldEmailReactions: 'https://netzhansa.com/jmap/email-reactions',
  HeroldShortcutCoach: 'https://netzhansa.com/jmap/shortcut-coach',
  HeroldPush: 'https://netzhansa.com/jmap/push',
  HeroldManagedRules: 'https://netzhansa.com/jmap/managed-rules',
  HeroldLLMTransparency: 'https://netzhansa.com/jmap/llm-transparency',
} as const;

export type CapabilityName = (typeof Capability)[keyof typeof Capability];

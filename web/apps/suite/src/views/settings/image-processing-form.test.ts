/**
 * REQ-EXTIMG-BG-32 / REQ-EXTIMG-BG-65 — settings → privacy "Image
 * processing" sub-section reads the JMAP session capability
 * `urn:netzhansa:params:jmap:internalize-status` and renders the
 * pending count + as-of timestamp. Hidden when pending_messages == 0.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import ImageProcessingForm from './ImageProcessingForm.svelte';

const CAP = 'urn:netzhansa:params:jmap:internalize-status';

const { mockAuth } = vi.hoisted(() => {
  const mockAuth = {
    status: 'ready' as const,
    session: {
      capabilities: {} as Record<string, unknown>,
      username: 'alice@example.local',
    },
  };
  return { mockAuth };
});

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: mockAuth,
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'settings.privacy.imageProcessing.heading': 'Image processing',
      'settings.privacy.imageProcessing.pending': `${String(args?.count ?? 0)} message is waiting for external images to be internalized.`,
      'settings.privacy.imageProcessing.pending.other': `${String(args?.count ?? 0)} messages are waiting for external images to be internalized.`,
      'settings.privacy.imageProcessing.asOf': `Counted ${String(args?.time ?? '')}.`,
      'time.justNow': 'just now',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en-GB',
}));

beforeEach(() => {
  vi.clearAllMocks();
  mockAuth.session.capabilities = {};
});

describe('ImageProcessingForm (REQ-EXTIMG-BG-32)', () => {
  it('renders the heading and pending count when pending_messages > 0', () => {
    mockAuth.session.capabilities[CAP] = {
      pending_messages: 5,
      as_of: new Date().toISOString(),
    };
    const { getByText } = render(ImageProcessingForm);
    expect(getByText('Image processing')).toBeInTheDocument();
    expect(
      getByText(
        '5 messages are waiting for external images to be internalized.',
      ),
    ).toBeInTheDocument();
  });

  it('renders the singular wording when pending_messages === 1', () => {
    mockAuth.session.capabilities[CAP] = {
      pending_messages: 1,
      as_of: new Date().toISOString(),
    };
    const { getByText } = render(ImageProcessingForm);
    expect(
      getByText(
        '1 message is waiting for external images to be internalized.',
      ),
    ).toBeInTheDocument();
  });

  it('hides the section entirely when pending_messages === 0', () => {
    mockAuth.session.capabilities[CAP] = {
      pending_messages: 0,
      as_of: new Date().toISOString(),
    };
    const { queryByText } = render(ImageProcessingForm);
    expect(queryByText('Image processing')).not.toBeInTheDocument();
  });

  it('hides the section when the capability is absent', () => {
    // No capability key set at all.
    const { queryByText } = render(ImageProcessingForm);
    expect(queryByText('Image processing')).not.toBeInTheDocument();
  });
});

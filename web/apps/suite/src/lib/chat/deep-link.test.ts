/**
 * Unit tests for handleOpenChatDeepLink.
 *
 * The function is pure (no module-level state, receives deps via arguments)
 * so these tests do not need mocked stores or a DOM.
 */

import { describe, it, expect, vi } from 'vitest';
import { handleOpenChatDeepLink } from './deep-link';
import type { DeepLinkDeps } from './deep-link';

function makeDeps(overrides: Partial<DeepLinkDeps> = {}): DeepLinkDeps {
  return {
    param: 'conv-123',
    conversationsReady: true,
    hasChatCap: true,
    onChatRoute: false,
    openWindow: vi.fn(),
    clearParam: vi.fn(),
    ...overrides,
  };
}

describe('handleOpenChatDeepLink', () => {
  it('calls openWindow and clearParam when all conditions are met', () => {
    const deps = makeDeps();
    const result = handleOpenChatDeepLink(deps);
    expect(result).toBe(true);
    expect(deps.openWindow).toHaveBeenCalledOnce();
    expect(deps.openWindow).toHaveBeenCalledWith('conv-123');
    expect(deps.clearParam).toHaveBeenCalledOnce();
  });

  it('is a no-op when param is null', () => {
    const deps = makeDeps({ param: null });
    const result = handleOpenChatDeepLink(deps);
    expect(result).toBe(false);
    expect(deps.openWindow).not.toHaveBeenCalled();
    expect(deps.clearParam).not.toHaveBeenCalled();
  });

  it('is a no-op when hasChatCap is false', () => {
    const deps = makeDeps({ hasChatCap: false });
    const result = handleOpenChatDeepLink(deps);
    expect(result).toBe(false);
    expect(deps.openWindow).not.toHaveBeenCalled();
  });

  it('is a no-op when on the fullscreen chat route', () => {
    const deps = makeDeps({ onChatRoute: true });
    const result = handleOpenChatDeepLink(deps);
    expect(result).toBe(false);
    expect(deps.openWindow).not.toHaveBeenCalled();
  });

  it('is a no-op when conversations are not yet loaded', () => {
    const deps = makeDeps({ conversationsReady: false });
    const result = handleOpenChatDeepLink(deps);
    expect(result).toBe(false);
    expect(deps.openWindow).not.toHaveBeenCalled();
  });

  it('passes the exact conversation id from the param', () => {
    const deps = makeDeps({ param: 'special-conv-abc-xyz' });
    handleOpenChatDeepLink(deps);
    expect(deps.openWindow).toHaveBeenCalledWith('special-conv-abc-xyz');
  });
});

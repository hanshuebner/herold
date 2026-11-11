import { describe, it, expect, vi } from 'vitest';
import { linkOutcomeKey, signInErrorKey, takeSignInError } from './oidc';

describe('oidc helpers', () => {
  it('maps known sign-in failure reasons and folds unknown ones into failed', () => {
    expect(signInErrorKey('not_linked')).toBe('login.oidc.error.not_linked');
    expect(signInErrorKey('browser_mismatch')).toBe('login.oidc.error.browser_mismatch');
    expect(signInErrorKey('<script>')).toBe('login.oidc.error.failed');
  });

  it('maps link outcomes and folds unknown ones into failed', () => {
    expect(linkOutcomeKey('linked')).toBe('settings.security.oidc.outcome.linked');
    expect(linkOutcomeKey('already_linked')).toBe('settings.security.oidc.outcome.already_linked');
    expect(linkOutcomeKey('bogus')).toBe('settings.security.oidc.outcome.failed');
  });

  it('takeSignInError returns and removes only oidc_error', () => {
    const hist = { state: null, replaceState: vi.fn() } as unknown as History;
    const loc = { search: '?a=1&oidc_error=expired', pathname: '/', hash: '#/mail' } as Location;
    expect(takeSignInError(loc, hist)).toBe('expired');
    expect(hist.replaceState).toHaveBeenCalledWith(null, '', '/?a=1#/mail');
  });

  it('takeSignInError leaves the URL alone when there is no error', () => {
    const hist = { state: null, replaceState: vi.fn() } as unknown as History;
    const loc = { search: '', pathname: '/', hash: '' } as Location;
    expect(takeSignInError(loc, hist)).toBeNull();
    expect(hist.replaceState).not.toHaveBeenCalled();
  });
});

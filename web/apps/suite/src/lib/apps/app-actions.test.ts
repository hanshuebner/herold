import { describe, it, expect } from 'vitest';
import {
  deriveAppActions,
  handoffUrl,
  isHandoffHash,
  isOpenableAppUrl,
  localizedText,
  normalizeAppActionIcon,
  parseHandoffHash,
} from './app-actions';
import { Capability, type SessionResource } from '../jmap/types';

function session(accountCaps: Record<string, Record<string, unknown>>): Pick<SessionResource, 'accounts'> {
  const accounts: SessionResource['accounts'] = {};
  for (const [id, caps] of Object.entries(accountCaps)) {
    accounts[id] = { name: id, isPersonal: true, isReadOnly: false, accountCapabilities: caps };
  }
  return { accounts };
}

const donation = {
  appId: '1',
  extensionId: 'donation-offer',
  point: 'thread.action',
  appName: { en: 'VzEkC donation offers', de: 'VzEkC-Spendenangebote' },
  label: { en: 'Create donation offer', de: 'Spendenangebot erstellen' },
  icon: 'gift',
};

describe('deriveAppActions', () => {
  it('lists thread actions of the account in capability order', () => {
    const s = session({
      A1: {
        [Capability.HeroldApps]: {
          actions: [donation, { ...donation, extensionId: 'second', icon: 'ticket' }],
        },
      },
    });
    const actions = deriveAppActions(s, 'A1', 'en');
    expect(actions.map((a) => a.extensionId)).toEqual(['donation-offer', 'second']);
    expect(actions[0]).toEqual({
      appId: '1',
      extensionId: 'donation-offer',
      point: 'thread.action',
      label: 'Create donation offer',
      appName: 'VzEkC donation offers',
      icon: 'gift',
    });
  });

  it('resolves labels in the current language', () => {
    const s = session({ A1: { [Capability.HeroldApps]: { actions: [donation] } } });
    const [a] = deriveAppActions(s, 'A1', 'de');
    expect(a?.label).toBe('Spendenangebot erstellen');
    expect(a?.appName).toBe('VzEkC-Spendenangebote');
  });

  it('falls back to en when the language is missing', () => {
    const s = session({
      A1: {
        [Capability.HeroldApps]: {
          actions: [{ ...donation, label: { en: 'Create donation offer' }, appName: { en: 'Forum' } }],
        },
      },
    });
    const [a] = deriveAppActions(s, 'A1', 'de');
    expect(a?.label).toBe('Create donation offer');
    expect(a?.appName).toBe('Forum');
  });

  it('falls back to the generic icon for a name outside the set', () => {
    const s = session({
      A1: { [Capability.HeroldApps]: { actions: [{ ...donation, icon: 'rocket' }, { ...donation, icon: undefined }] } },
    });
    expect(deriveAppActions(s, 'A1', 'en').map((a) => a.icon)).toEqual(['app', 'app']);
  });

  it('is empty without the capability, without actions or for another account', () => {
    expect(deriveAppActions(session({ A1: {} }), 'A1', 'en')).toEqual([]);
    expect(deriveAppActions(session({ A1: { [Capability.HeroldApps]: { actions: [] } } }), 'A1', 'en')).toEqual([]);
    expect(
      deriveAppActions(session({ A1: { [Capability.HeroldApps]: { actions: [donation] } } }), 'A2', 'en'),
    ).toEqual([]);
    expect(deriveAppActions(null, 'A1', 'en')).toEqual([]);
    expect(deriveAppActions(session({ A1: {} }), null, 'en')).toEqual([]);
  });

  it('skips actions of other points and malformed entries', () => {
    const s = session({
      A1: {
        [Capability.HeroldApps]: {
          actions: [
            { ...donation, point: 'message.action' },
            { ...donation, appId: 7 },
            { ...donation, label: {} },
            null,
            donation,
          ],
        },
      },
    });
    expect(deriveAppActions(s, 'A1', 'en')).toHaveLength(1);
  });
});

describe('localizedText', () => {
  it('prefers the exact tag, then the primary subtag, then en', () => {
    const m = { en: 'E', de: 'D', 'de-CH': 'CH' };
    expect(localizedText(m, 'de-CH')).toBe('CH');
    expect(localizedText({ en: 'E', de: 'D' }, 'de-AT')).toBe('D');
    expect(localizedText({ en: 'E' }, 'fr')).toBe('E');
    expect(localizedText({ fr: 'F' }, 'de')).toBe('F');
    expect(localizedText(undefined, 'en')).toBe('');
  });
});

describe('normalizeAppActionIcon', () => {
  it('keeps every icon of the closed set', () => {
    for (const n of ['app', 'gift', 'ticket', 'calendar-plus', 'user-plus', 'archive', 'send', 'link', 'file-plus']) {
      expect(normalizeAppActionIcon(n)).toBe(n);
    }
    expect(normalizeAppActionIcon('https://evil.example/x.svg')).toBe('app');
  });
});

describe('isOpenableAppUrl', () => {
  it('accepts https always', () => {
    expect(isOpenableAppUrl('https://forum.example/d/1', false)).toBe(true);
  });

  it('accepts http only on loopback hosts in dev builds', () => {
    expect(isOpenableAppUrl('http://127.0.0.1:9000/opened/x', true)).toBe(true);
    expect(isOpenableAppUrl('http://localhost:9000/opened/x', true)).toBe(true);
    expect(isOpenableAppUrl('http://127.0.0.1:9000/opened/x', false)).toBe(false);
    expect(isOpenableAppUrl('http://forum.example/x', true)).toBe(false);
  });

  it('refuses other schemes and unparsable URLs', () => {
    expect(isOpenableAppUrl('javascript:alert(1)', true)).toBe(false);
    expect(isOpenableAppUrl('data:text/html,hi', true)).toBe(false);
    expect(isOpenableAppUrl('not a url', true)).toBe(false);
  });
});

describe('handoff route', () => {
  it('round-trips the app name and language through the hash', () => {
    const url = handoffUrl({ origin: 'https://mail.example', pathname: '/' }, 'Forum & Co', 'de');
    expect(url).toBe('https://mail.example/#/app-handoff?app=Forum%20%26%20Co&lang=de');
    const hash = new URL(url).hash;
    expect(isHandoffHash(hash)).toBe(true);
    expect(parseHandoffHash(hash)).toEqual({ appName: 'Forum & Co', lang: 'de' });
  });

  it('does not match other routes', () => {
    expect(isHandoffHash('#/mail')).toBe(false);
    expect(isHandoffHash('#/app-handoff-x')).toBe(false);
    expect(isHandoffHash('')).toBe(false);
  });
});

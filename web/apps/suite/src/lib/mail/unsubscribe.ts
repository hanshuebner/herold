/**
 * RFC 8058 one-click unsubscribe POST (REQ-UNS-20).
 *
 * The request carries no cookies and no referrer -- `credentials:
 * "omit"` per the threat model in
 * docs/design/web/requirements/14-unsubscribe.md, `referrerPolicy:
 * "no-referrer"` so the target learns nothing about the message the
 * user read it from. A browser `fetch()` cannot suppress its own
 * User-Agent header (it is a forbidden header per the Fetch spec), so
 * that part of REQ-UNS-20 is honoured to the extent the platform
 * allows.
 */
export interface OneClickResult {
  ok: boolean;
}

export async function postOneClickUnsubscribe(url: string): Promise<OneClickResult> {
  try {
    const res = await fetch(url, {
      method: 'POST',
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
      },
      body: 'List-Unsubscribe=One-Click',
    });
    return { ok: res.ok };
  } catch {
    return { ok: false };
  }
}

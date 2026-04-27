// background.js — service worker
// Handles messages from popup (export, clear).

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg.type === 'GET_EVENTS') {
    chrome.storage.local.get(['events'], (res) => {
      sendResponse({ events: res.events || [] });
    });
    return true; // async
  }

  if (msg.type === 'CLEAR_EVENTS') {
    chrome.storage.local.set({ events: [] }, () => sendResponse({ ok: true }));
    return true;
  }
});

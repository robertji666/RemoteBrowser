chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message && message.type === 'remoteBrowserFullscreen') {
    fetch('http://127.0.0.1:6084/state', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ fullscreen: message.fullscreen === true })
    }).catch(() => {});
    return false;
  }
	if (!message || message.type !== 'remoteBrowserDownload') return false;

  let parsed;
  try {
    parsed = new URL(message.url);
  } catch (_error) {
    sendResponse({ ok: false, error: 'invalid download URL' });
    return false;
  }

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    sendResponse({ ok: false, error: 'unsupported download URL' });
    return false;
  }

  chrome.downloads.download({
    url: parsed.href,
    saveAs: false,
    conflictAction: 'uniquify'
  }, (downloadId) => {
    if (chrome.runtime.lastError) {
      sendResponse({ ok: false, error: chrome.runtime.lastError.message });
      return;
    }
    sendResponse({ ok: true, downloadId });
  });
  return true;
});

(() => {
  const downloadWords = /(^|[\s_-])(download|下载|下載)([\s_-]|$)/i;

  // The remote manager needs to know about webpage fullscreen (for example,
  // YouTube's player fullscreen), which does not change the X11 window state.
  // Report only from the top document so an embedded frame cannot overwrite
  // the page's fullscreen state with a spurious `false` event.
  if (window.top === window) {
    let heartbeat = null;
    const reportFullscreen = () => {
      const fullscreen = Boolean(document.fullscreenElement || document.webkitFullscreenElement);
      chrome.runtime.sendMessage({
        type: 'remoteBrowserFullscreen',
        fullscreen
      });
      if (fullscreen && !heartbeat) heartbeat = window.setInterval(reportFullscreen, 1000);
      if (!fullscreen && heartbeat) {
        window.clearInterval(heartbeat);
        heartbeat = null;
      }
    };
    document.addEventListener('fullscreenchange', reportFullscreen, true);
    document.addEventListener('webkitfullscreenchange', reportFullscreen, true);
    reportFullscreen();
  }

  function hasDownloadIntent(anchor) {
    if (anchor.hasAttribute('download')) return true;
    const marker = [
      anchor.textContent,
      anchor.getAttribute('aria-label'),
      anchor.getAttribute('title'),
      anchor.id,
      anchor.className
    ].filter((value) => typeof value === 'string').join(' ').trim();
    return downloadWords.test(marker);
  }

  function showNotice(text, isError = false) {
    const oldNotice = document.getElementById('remote-browser-download-notice');
    if (oldNotice) oldNotice.remove();

    const notice = document.createElement('div');
    notice.id = 'remote-browser-download-notice';
    notice.textContent = text;
    Object.assign(notice.style, {
      position: 'fixed',
      zIndex: '2147483647',
      top: '16px',
      right: '16px',
      maxWidth: '360px',
      padding: '10px 14px',
      borderRadius: '8px',
      color: '#fff',
      background: isError ? '#b42318' : '#067647',
      boxShadow: '0 6px 20px rgba(0,0,0,.28)',
      font: '14px/1.4 system-ui, sans-serif'
    });
    (document.body || document.documentElement).appendChild(notice);
    window.setTimeout(() => notice.remove(), 3500);
  }

  document.addEventListener('click', (event) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }

    const target = event.target;
    if (!(target instanceof Element)) return;
    const anchor = target.closest('a[href]');
    if (!anchor || !hasDownloadIntent(anchor)) return;

    let url;
    try {
      url = new URL(anchor.href, document.baseURI);
    } catch (_error) {
      return;
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return;

    event.preventDefault();
    event.stopImmediatePropagation();
    chrome.runtime.sendMessage({ type: 'remoteBrowserDownload', url: url.href }, (response) => {
      if (chrome.runtime.lastError || !response || !response.ok) {
        const error = chrome.runtime.lastError?.message || response?.error || '未知错误';
        showNotice(`下载启动失败：${error}`, true);
        return;
      }
      showNotice('下载已开始，完成后会出现在 RemoteBrowser 文件列表中');
    });
  }, true);
})();

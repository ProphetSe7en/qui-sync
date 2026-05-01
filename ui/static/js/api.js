// Global fetch wrapper. Handles two cross-cutting concerns so call sites
// don't have to:
//
// (1) CSRF — attach X-CSRF-Token header to every mutating fetch. Server
//     sets a quisync_csrf cookie on GETs; the wrapper reads the cookie
//     and echoes it as a header on POST/PUT/DELETE/PATCH. AJAX only —
//     <form method="POST"> uses a hidden csrf_token field (logout form).
//
// (2) 401 → /login redirect — if any auth-gated endpoint returns 401
//     (session expired, logged out elsewhere, cookie cleared), bounce
//     the user to the login page. Centralized so every fetch call site
//     doesn't need `if (resp.status === 401) window.location...` boiler-
//     plate. A never-resolving promise is returned on redirect so
//     callers don't try to .json() a body that won't arrive before the
//     navigation completes.
//
//     Skip the redirect when:
//       - Already on /login or /setup (avoid loop — these pages probe
//         /api/auth/status as a public endpoint).
//       - Caller opts out via X-Skip-Login-Redirect header. The only
//         legitimate use is an inline confirm-password modal, where
//         401 means "wrong password" not "session expired".
(function() {
  const origFetch = window.fetch.bind(window);
  window.fetch = async function(input, init) {
    const request = new Request(input, init);
    const method = request.method.toUpperCase();
    const headers = new Headers(request.headers);
    if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
      const m = document.cookie.match(/(?:^|; )quisync_csrf=([^;]+)/);
      if (m) headers.set('X-CSRF-Token', m[1]);
    }
    const skipLoginRedirect = headers.get('X-Skip-Login-Redirect') === '1';
    headers.delete('X-Skip-Login-Redirect');
    const resp = await origFetch(new Request(request, { headers }));
    if (resp.status === 401 && !skipLoginRedirect) {
      const path = window.location.pathname;
      if (path !== '/login' && path !== '/setup') {
        window.location.href = '/login';
        return new Promise(() => {});
      }
    }
    return resp;
  };
})();

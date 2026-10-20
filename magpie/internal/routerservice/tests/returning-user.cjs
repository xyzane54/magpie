'use strict';
// Existing page suites exercise configured/returning users. The dedicated
// tutorial suite keeps fresh contexts and owns first-visit behavior.
const initialized = new WeakSet();
async function skipTutorial(page) {
  if (initialized.has(page)) return;
  initialized.add(page);
  await page.addInitScript(() => {
    localStorage.setItem('api-router.tutorial.v1', JSON.stringify({
      version: 1, step: 5, route: 'cloud', app: 'workbuddy',
      dismissed: true, completed: true
    }));
  });
}
module.exports = { skipTutorial };

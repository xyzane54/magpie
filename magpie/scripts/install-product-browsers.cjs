// Provisioning only; the regression gate itself never downloads dependencies.
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const root = path.resolve(__dirname, '..');
const cache = path.resolve(process.env.PLAYWRIGHT_BROWSERS_PATH || path.join(root, 'dist/cache/playwright'));
const args = process.argv.slice(2);
if (args.some(arg => arg !== '--with-deps')) {
  console.error('Only --with-deps is accepted (it installs system packages on Linux).');
  process.exit(1);
}
console.log(`Browser dependency cache: ${cache}`);
const result = spawnSync(process.execPath, [path.join(root, 'node_modules/playwright/cli.js'), 'install', ...args, 'chromium', 'webkit'], {
  cwd: root, env: { ...process.env, PLAYWRIGHT_BROWSERS_PATH: cache }, stdio: 'inherit', windowsHide: true
});
if (result.error) console.error(result.error.message);
process.exitCode = result.status ?? 1;

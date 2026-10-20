// Platform-neutral product gate. Dependency provisioning is deliberately separate:
// this command runs offline and never installs a tool or changes the host.
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const crypto = require('node:crypto');
const {withHeavyLock,repositoryGitDir} = require('./heavy-regression-lock.cjs');
const { spawn, execFileSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const packages = [
  './cmd/api-router', './internal/routerservice', './internal/routerconfig',
  './internal/routerrequest', './internal/routercloud', './internal/cloudcodec', './internal/routerredact',
  './internal/routerledger', './internal/routerobserve', './internal/routerhost', './internal/routerdevice',
  './internal/routermanifest', './internal/routerdownload', './internal/routerprocess',
  './internal/routerprocess/fakeserver', './internal/routerruntime', './cmd/fake-llama-server',
  './cmd/model-fetch', './cmd/real-model-check', './internal/routeracceptance',
  './internal/dirs', './internal/steady', './internal/edit', './internal/filememo',
  './internal/routercontrol', './internal/routerdelivery'
];
const stages = ['build', 'race', 'vet', 'chromium', 'webkit'];
const requiredBrowserFiles = ['page.test.cjs', 'provider-presets.test.cjs', 'profile-probe.test.cjs', 'observations.test.cjs', 'local-model.test.cjs', 'loomy-host.test.cjs', 'redaction.test.cjs', 'service-control.test.cjs', 'spark-models.test.cjs', 'tutorial.test.cjs'];
const systemVariables = /^(PATH|PATHEXT|SYSTEMROOT|WINDIR|COMSPEC|SYSTEMDRIVE|LANG|LC_ALL|TZ|CC|CXX|LD_LIBRARY_PATH|DYLD_LIBRARY_PATH|DISPLAY|WAYLAND_DISPLAY|XDG_RUNTIME_DIR|PLAYWRIGHT_HOST_PLATFORM_OVERRIDE)$/i;

function isolatedEnv(source, temporary, caches) {
  // An allow-list prevents inherited credentials, host overrides, proxies,
  // NODE_OPTIONS, GOFLAGS and cross-compilation settings from changing the run.
  const env = Object.fromEntries(Object.entries(source).filter(([key]) => systemVariables.test(key)));
  for (const name of ['HOME', 'USERPROFILE']) env[name] = path.join(temporary, 'home');
  for (const name of ['XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA']) env[name] = path.join(temporary, name);
  for (const name of ['TMP', 'TEMP', 'TMPDIR']) env[name] = path.join(temporary, 'tmp');
  for (const folder of new Set(Object.values(env).filter(value => typeof value === 'string' && value.startsWith(temporary)))) fs.mkdirSync(folder, { recursive: true });
  return { ...env, ...caches, GOTOOLCHAIN: 'local', GOENV: 'off', GOWORK: 'off',
    GOPROXY: 'off', GOSUMDB: 'off', GOFLAGS: '-mod=readonly -buildvcs=false' };
}

function command(file, args, env, log) {
  return new Promise((resolve, reject) => {
    fs.appendFileSync(log, `${JSON.stringify([file, ...args])}\n`);
    const child = spawn(file, args, { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
    child.stdout.on('data', data => { fs.appendFileSync(log, data); process.stdout.write(data); });
    child.stderr.on('data', data => { fs.appendFileSync(log, data); process.stderr.write(data); });
    const int = () => child.kill('SIGINT'), term = () => child.kill('SIGTERM');
    process.on('SIGINT', int); process.on('SIGTERM', term);
    const cleanup = () => {process.removeListener('SIGINT',int);process.removeListener('SIGTERM',term);};
    child.on('error', error => {cleanup();reject(error);});
    child.on('close', (code, signal) => {cleanup(); code === 0 ? resolve() : reject(new Error(`${file} exited ${code ?? signal}; see ${log}`));});
  });
}

function goEnvironment(source) {
  // Resolve caches before replacing HOME; never let each test download another
  // toolchain/module cache. GOENV=off avoids reading the user's Go config.
  const cacheVariables = /^(HOME|USERPROFILE|APPDATA|LOCALAPPDATA|XDG_CACHE_HOME|GOPATH|GOMODCACHE|GOCACHE)$/i;
  const env = { ...Object.fromEntries(Object.entries(source).filter(([key]) => systemVariables.test(key) || cacheVariables.test(key))),
    GOTOOLCHAIN: 'local', GOENV: 'off', GOWORK: 'off', GOFLAGS: '', GOPROXY: 'off', GOSUMDB: 'off' };
  let info;
  try {
    info = JSON.parse(execFileSync('go', ['env', '-json', 'GOVERSION', 'GOPATH', 'GOMODCACHE', 'GOCACHE'], { cwd: root, env, encoding: 'utf8' }));
  } catch (error) {
    throw new Error(`Go unavailable: install Go ${goVersion()} on this runner and add go to PATH (${error.code || error.message})`);
  }
  if (info.GOVERSION !== `go${goVersion()}`) throw new Error(`Go ${goVersion()} required; found ${info.GOVERSION}. Automatic toolchain downloads are disabled.`);
  for (const key of ['GOPATH', 'GOMODCACHE', 'GOCACHE']) {
    if (!path.isAbsolute(info[key])) throw new Error(`${key} must be an absolute pinned cache path: ${info[key]}`);
  }
  return info;
}

function goVersion() { return fs.readFileSync(path.join(root, 'go.mod'), 'utf8').match(/^go (\S+)$/m)[1]; }

async function browserPreflight(engine, env, log) {
  const required = require('../package.json').devDependencies.playwright;
  let playwright;
  try {
    // Resolve only this checkout's locked dependency, never NODE_PATH's copy.
    const manifest = require('../node_modules/playwright/package.json');
    if (manifest.version !== required) throw new Error(`expected ${required}, got ${manifest.version}`);
    playwright = path.join(root, 'node_modules/playwright');
  } catch (error) {
    throw new Error(`Playwright ${required} unavailable: run npm ci in magpie (${error.message})`);
  }
  const probe = `const p=require(${JSON.stringify(playwright)}); (async()=>{const b=await p[${JSON.stringify(engine)}].launch(); console.log(JSON.stringify({engine:${JSON.stringify(engine)},version:b.version(),executable:p[${JSON.stringify(engine)}].executablePath()})); await b.close()})().catch(e=>{console.error(e);process.exitCode=1})`;
  try { await command(process.execPath, ['-e', probe], env, log); }
  catch (error) { throw new Error(`${engine} did not launch on ${process.platform}/${process.arch}. Install its pinned browser and OS dependencies with the documented provisioning command; PLAYWRIGHT_BROWSERS_PATH=${env.PLAYWRIGHT_BROWSERS_PATH}. ${error.message}`); }
}

function candidateState(dependencies = {}) {
  const candidate = {};
  try {
    const execute = dependencies.execFileSync || execFileSync;
    const platform = dependencies.platform || process.platform;
    const kernel = dependencies.kernel || os.release();
    const directory = dependencies.gitDirectory || repositoryGitDir();
    const worktree = dependencies.worktree || path.resolve(root,'..');
    const markerPath = path.join(worktree, '.git');
    const marker = dependencies.gitMarker ?? (fs.statSync(markerPath).isFile() ? fs.readFileSync(markerPath, 'utf8') : '');
    // A Windows-created worktree shares its Windows index with WSL. Native
    // Linux Git invalidates those stat entries and can spend minutes rereading
    // every tracked file on DrvFS. The owning platform's Git checks the same
    // complete tree without rewriting or manufacturing index state.
    const windowsIndex = platform === 'linux' && /microsoft/i.test(kernel) && /^gitdir:\s*[A-Za-z]:[\\/]/.test(marker);
    const windowsPath = value => {
      const matched = /^\/mnt\/([a-z])\/(.*)$/i.exec(value);
      if (!matched) throw new Error('Windows worktree path is not mounted under /mnt/<drive>');
      return matched[1].toUpperCase() + ':/' + matched[2];
    };
    candidate.gitExecutable = windowsIndex ? 'git.exe' : 'git';
    candidate.gitPlatform = windowsIndex ? 'win32' : platform;
    const args = windowsIndex
      ? ['--git-dir='+windowsPath(directory), '--work-tree='+windowsPath(worktree)]
      : ['--git-dir='+directory, '--work-tree='+worktree];
    const git = rest => execute(candidate.gitExecutable,[...args,...rest],{cwd:root,encoding:'utf8',stdio:['ignore','pipe','pipe'],timeout:rest[0] === 'status' ? 60000 : 15000,env:{...process.env,GIT_OPTIONAL_LOCKS:'0'}}).trim();
    candidate.sha=git(['rev-parse','HEAD']);
    try {candidate.trackedDirty=git(['status','--porcelain','--untracked-files=no'])!=='';}
    catch {candidate.trackedDirty=null;candidate.reason='Git working-tree status unavailable or timed out';}
  } catch {candidate.sha=null;candidate.reason='Git metadata unavailable';}
  return candidate;
}

async function runRegression(options = {}, dependencies = {}) {
  const run = dependencies.command || command;
  const prepareGo = dependencies.goEnvironment || goEnvironment;
  const prepareBrowser = dependencies.browserPreflight || browserPreflight;
  const source = options.env || process.env;
  const selected = options.only ? options.only.split(',') : stages;
  if (selected.some(stage => !stages.includes(stage))) throw new Error(`--only accepts ${stages.join(',')}`);
  // Browser diagnostics always build their own matching binary first.
  const requested = new Set(selected);
  if (selected.some(stage => ['chromium', 'webkit'].includes(stage))) requested.add('build');
  const base = path.join(root, 'dist', 'regression');
  fs.mkdirSync(base, { recursive: true });
  const output = options.output ? path.resolve(options.output) : fs.mkdtempSync(path.join(base, 'run-'));
  if (options.output) fs.mkdirSync(output); // Refuse to overwrite old evidence.
  // SQLite locks and child-process fixtures need the runner's native temporary
  // filesystem, even when the checkout/output is on a WSL or network mount.
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'api-router-regression-'));
  const binary = path.join(output, process.platform === 'win32' ? 'api-router.exe' : 'api-router');
  const browserFiles = fs.readdirSync(path.join(root, 'internal/routerservice/tests')).filter(name => name.endsWith('.test.cjs')).sort().map(name => `internal/routerservice/tests/${name}`);
  const wsl = process.platform === 'linux' && /microsoft/i.test(os.release());
  const summary = { schemaVersion: 1, started: new Date().toISOString(), platform: process.platform,
    arch: process.arch, environment: wsl ? 'WSL (Linux)' : 'native', kernel: os.release(), osVersion: os.version(), node: process.version,
    goRequired: goVersion(), playwrightRequired: require('../package.json').devDependencies.playwright,
    candidate: candidateState(), output, temporary, binary, packages, browserFiles, complete: false, status: 'running',
    platforms: Object.fromEntries(['win32', 'darwin', 'linux'].map(platform => [platform, platform === process.platform ? 'this runner only' : 'not run: no corresponding runner in this invocation'])),
    steps: stages.map(name => ({ name, status: 'not_run', reason: requested.has(name) ? 'not reached' : 'not requested (--only diagnostic run)' })) };
  let failed = false;
  const save = () => fs.writeFileSync(path.join(output, 'summary.json'), JSON.stringify(summary, null, 2) + '\n');
  save(); // An interrupted invocation must never look like a completed run.
  console.log(`Product regression: ${process.platform}/${process.arch}${wsl ? ' WSL (Linux)' : ''}\nOutput: ${output}`);
  try {
    if (![22, 24].includes(Number(process.versions.node.split('.')[0]))) throw new Error('Node 22 or 24 required; provision .node-version for the reproducible baseline');
    const go = prepareGo(source);
    summary.go = go.GOVERSION;
    summary.caches = { GOPATH: go.GOPATH, GOMODCACHE: go.GOMODCACHE, GOCACHE: go.GOCACHE,
      PLAYWRIGHT_BROWSERS_PATH: path.resolve(source.PLAYWRIGHT_BROWSERS_PATH || path.join(root, 'dist/cache/playwright')) };
    const env = isolatedEnv(source, temporary, summary.caches);
    env.API_ROUTER_BINARY = binary;
    for (const step of summary.steps) {
      if (!requested.has(step.name)) continue;
      if (options.lockControl?.interrupted()) throw new Error('Regression interrupted');
      step.log = path.join(output, `${step.name}.log`);
      step.status = 'running'; delete step.reason;
      save();
      const started = Date.now();
      try {
        if (step.name === 'build') {
          await run('go', ['build', '-trimpath', '-ldflags=-s -w -X main.version=0.1.0-dev+regression', '-o', binary, './cmd/api-router'], { ...env, CGO_ENABLED: '0' }, step.log);
          if (fs.existsSync(binary)) summary.binarySHA256 = crypto.createHash('sha256').update(fs.readFileSync(binary)).digest('hex');
        } else if (step.name === 'race') {
          await run('go', ['test', '-race', '-count=1', '-p', '1', '-timeout', '45m', ...packages], { ...env, CGO_ENABLED: '1' }, step.log);
        } else if (step.name === 'vet') {
          await run('go', ['vet', ...packages], { ...env, CGO_ENABLED: '0' }, step.log);
        } else {
          const browserEnv = { ...env, BROWSER: step.name, ARTIFACT_DIR: path.join(output, step.name) };
          if (!requiredBrowserFiles.every(name => browserFiles.includes(`internal/routerservice/tests/${name}`))) throw new Error('one of the required product page suites is missing');
          await prepareBrowser(step.name, browserEnv, step.log);
          await run(process.execPath, ['--test', '--test-concurrency=1', '--test-reporter=tap', ...browserFiles], browserEnv, step.log);
        }
        if (options.lockControl?.interrupted()) throw new Error('Regression interrupted');
        step.status = 'passed';
      } catch (error) {
        step.status = 'failed'; step.reason = error.message;
        fs.appendFileSync(step.log, `\nFAILED: ${error.message}\n`);
        throw error;
      } finally { step.durationMs = Date.now() - started; save(); }
    }
    if (options.lockControl?.interrupted()) throw new Error('Regression interrupted');
    summary.complete = summary.steps.every(step => step.status === 'passed');
  } catch (error) {
    failed = true; summary.error = error.message;
    for (const step of summary.steps) if (step.status === 'not_run' && requested.has(step.name)) step.reason = `blocked: ${error.message}`;
    console.error(error.message);
  } finally {
    // Only our newly created run directory contains disposable state.
    try { fs.rmSync(temporary, { recursive: true, force: true }); }
    catch (error) { failed = true; summary.cleanupError = error.message; }
    summary.finished = new Date().toISOString();
    summary.complete = summary.complete && !failed;
    summary.status = failed ? 'failed' : summary.complete ? 'passed' : 'partial';
    save();
    for (const step of summary.steps) console.log(`${step.name}: ${step.status}${step.reason ? ` (${step.reason})` : ''}`);
    console.log(`Result: ${summary.status}; ${path.join(output, 'summary.json')}`);
  }
  return { code: failed ? 1 : 0, summary };
}

if (require.main === module) {
  const options = {};
  for (const arg of process.argv.slice(2)) {
    if (arg.startsWith('--only=')) options.only = arg.slice(7);
    else if (arg.startsWith('--output=')) options.output = arg.slice(9);
    else { console.error(`Unknown argument: ${arg}`); process.exit(1); }
  }
  withHeavyLock(async control => {
    const result = await runRegression({...options, lockControl: control});
    return result;
  }).then(result => { process.exitCode = result.code; }).catch(error => { console.error(error.message); process.exitCode = 1; });
}
module.exports = { runRegression, isolatedEnv, packages, command, candidateState };
